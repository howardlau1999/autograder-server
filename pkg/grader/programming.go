package grader

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	autograder_grpc "autograder-server/pkg/grader/grpc"
	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

type graderLoggerCtxKey struct{}

func SetGraderLogger(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, graderLoggerCtxKey{}, logger)
}

func GetGraderLogger(ctx context.Context) *zap.Logger {
	logger := zap.L()
	if ctxLogger, ok := ctx.Value(graderLoggerCtxKey{}).(*zap.Logger); ok {
		logger = ctxLogger
	}
	return logger
}

type ProgrammingGrader interface {
	PullImage(image string) error
	GradeSubmission(
		ctx context.Context,
		submissionId uint64, submission *model_pb.Submission, config *model_pb.ProgrammingAssignmentConfig,
		notifyC chan *grader_pb.GradeReport,
	)
}

type HubGrader struct {
	hubService *autograder_grpc.GraderHubService
}

func (h *HubGrader) PullImage(image string) error {
	return nil
}

func (h *HubGrader) GradeSubmission(
	ctx context.Context,
	submissionId uint64, submission *model_pb.Submission, config *model_pb.ProgrammingAssignmentConfig,
	notifyC chan *grader_pb.GradeReport,
) {
	request := &grader_pb.GradeRequest{
		SubmissionId: submissionId,
		Submission:   submission,
		Config:       config,
	}
	_, err := h.hubService.Grade(ctx, request)
	if err != nil {
		zap.L().Error("GraderHub.GradeSubmission", zap.Error(err))
	}
	if notifyC != nil {
		h.hubService.SubscribeSubmission(submissionId, notifyC)
	}
}

type DockerProgrammingGrader struct {
	cli         *client.Client
	concurrency int
	running     int
	mu          *sync.Mutex
	cond        *sync.Cond
}

func truncateOutput(output string, maxLen int, prompt string) string {
	outputLen := len(output)
	if outputLen <= maxLen {
		return output
	}
	halfLen := (maxLen - len(prompt)) / 2
	return output[:halfLen] + prompt + output[outputLen-halfLen:]
}

func (d *DockerProgrammingGrader) StreamLog(ctx context.Context, containerId string) (io.ReadCloser, error) {
	r, err := d.cli.ContainerLogs(
		ctx, containerId,
		types.ContainerLogsOptions{Follow: true, Tail: "300", ShowStderr: true, ShowStdout: true},
	)
	return r, err
}

func (d *DockerProgrammingGrader) PullImage(image string) error {
	closer, err := d.cli.ImagePull(context.Background(), image, types.ImagePullOptions{})
	if err != nil {
		return err
	}
	_, err = ioutil.ReadAll(closer)
	return err
}

func (d *DockerProgrammingGrader) BuildImage(ctx context.Context, buildContext io.Reader, image string) error {
	d.cli.ImageBuild(ctx, buildContext, types.ImageBuildOptions{Context: buildContext, Tags: []string{image}})
	return nil
}

const (
	ErrListImage             = -1
	ErrPullImage             = 1
	ErrReadPullImageResponse = 2
	ErrCreateContainer       = 3
	ErrStartContainer        = 4
	ErrWaitContainer         = 5
	ErrInspectContainer      = 6
	ErrTimeout               = 7
	ErrOpenResultJSON        = 8
	ErrReadResultJSON        = 9
	ErrUnmarshalResultJSON   = 10
)

func (d *DockerProgrammingGrader) runDocker(
	ctx context.Context, basePath string, submissionId uint64, submission *model_pb.Submission,
	config *model_pb.ProgrammingAssignmentConfig, cpusetMems string, containerIdCh chan string,
	containerStartCh chan bool,
) (internalError int64, exitCode int64, resultsJSONPath string, err error) {
	defer close(containerIdCh)
	defer close(containerStartCh)

	logger := GetGraderLogger(ctx).With(zap.Uint64("submissionId", submissionId), zap.String("image", config.Image))
	logger.Debug("Docker.Run.Enter")
	defer logger.Debug("Docker.Run.Exit")
	var pullProgress io.ReadCloser
	pullProgress, err = d.cli.ImagePull(ctx, config.Image, types.ImagePullOptions{})
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		internalError = ErrPullImage
		logger.Error("Docker.Run.PullImage", zap.Error(err))
		return
	}
	pullProgressBuf := make([]byte, 32*1024, 32*1024)
	var n int
	for {
		n, err = pullProgress.Read(pullProgressBuf)
		if err != nil {
			if err == io.EOF {
				break
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				return
			}
			internalError = ErrReadPullImageResponse
			logger.Error("Docker.Run.PullImage.ReadAll", zap.Error(err))
			return
		}
		logger.Debug("Docker.Run.PullImage.Progress", zap.ByteString("progress", pullProgressBuf[:n]))
	}
	containerConfig := &container.Config{
		Hostname:     "",
		Domainname:   "",
		User:         "",
		AttachStdin:  false,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		OpenStdin:    false,
		StdinOnce:    false,
		Env:          nil,
		Entrypoint: []string{
			"sh", "-c", "/autograder/run",
		},
		Image:      config.Image,
		Volumes:    nil,
		WorkingDir: "/autograder",
	}

	runDir := filepath.Join(basePath, fmt.Sprintf("runs/submissions/%d", submissionId))
	resultsDir := filepath.Join(runDir, "results")
	resultsJSONPath = filepath.Join(resultsDir, "results.json")
	outputsDir := filepath.Join(resultsDir, "outputs")
	subDir := filepath.Join(basePath, submission.Path)
	os.RemoveAll(runDir)
	os.MkdirAll(runDir, 0755)
	os.MkdirAll(resultsDir, 0755)
	os.MkdirAll(outputsDir, 0755)
	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: subDir, Target: "/autograder/submission"},
			{Type: mount.TypeBind, Source: resultsDir, Target: "/autograder/results"},
		},
	}
	if cpusetMems != "" {
		hostConfig.CpusetMems = cpusetMems
	}
	if config.GetCpu() > 0 {
		hostConfig.CPUQuota = int64(math.Round(float64(100000 * config.GetCpu())))
		hostConfig.CPUShares = int64(math.Round(float64(1024 * config.GetCpu())))
	}
	if config.GetMemory() > 0 {
		hostConfig.Memory = config.GetMemory()
	}
	networkingConfig := &network.NetworkingConfig{}
	platform := &specs.Platform{OS: "linux"}
	var body container.ContainerCreateCreatedBody
	body, err = d.cli.ContainerCreate(
		ctx,
		containerConfig, hostConfig, networkingConfig, platform, fmt.Sprintf("submission-%d", submissionId),
	)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		internalError = ErrCreateContainer
		logger.Error("Docker.Run.CreateContainer", zap.Error(err))
		return
	}
	containerId := body.ID
	logger = logger.With(zap.String("containerId", containerId))

	defer d.RemoveContainerBackground(logger, containerId)
	logger.Debug("Docker.Run.ContainerCreated")
	containerIdCh <- containerId
	doneC, errC := d.cli.ContainerWait(ctx, containerId, container.WaitConditionNextExit)
	err = d.cli.ContainerStart(ctx, containerId, types.ContainerStartOptions{})
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		internalError = ErrStartContainer
		logger.Error("Docker.Run.StartContainer")
		return
	}
	containerStartCh <- true
	logger.Debug("Docker.Run.ContainerStarted")
	timeout := 5 * time.Minute
	if config.GetTimeout() > 0 {
		timeout = time.Duration(config.GetTimeout()) * time.Second
	}
	ticker := time.NewTicker(timeout)
	defer ticker.Stop()
	select {
	case err = <-errC:
		if ctx.Err() != nil {
			err = ctx.Err()
			return
		}
		internalError = ErrWaitContainer
		logger.Error("Docker.Run.WaitContainer")
		return
	case <-ctx.Done():
		err = ctx.Err()
		return
	case <-ticker.C:
		internalError = ErrTimeout
		return
	case <-doneC:
		logger.Debug("Docker.Run.ContainerExited")
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var containerDetail types.ContainerJSON
	containerDetail, err = d.cli.ContainerInspect(ctx, containerId)
	if err != nil {
		internalError = ErrInspectContainer
		logger.Error("Docker.Run.InspectContainer", zap.Error(err))
		return
	}
	exitCode = int64(containerDetail.State.ExitCode)
	return
}

func (d *DockerProgrammingGrader) RemoveContainerBackground(logger *zap.Logger, containerId string) {
	go d.RemoveContainer(context.Background(), logger, containerId)
}

func (d *DockerProgrammingGrader) RemoveContainer(ctx context.Context, logger *zap.Logger, containerId string) error {
	if err := d.cli.ContainerRemove(
		ctx, containerId, types.ContainerRemoveOptions{Force: true},
	); err != nil {
		logger.Error("Docker.Run.RemoveContainer", zap.Error(err))
		return err
	}
	logger.Debug("Docker.Run.ContainerRemoved")
	return nil
}

func (d *DockerProgrammingGrader) GradeSubmission(
	ctx context.Context, basePath string,
	submissionId uint64, submission *model_pb.Submission,
	config *model_pb.ProgrammingAssignmentConfig, cpusetMems string, notifyC chan *grader_pb.GradeReport,
) {
	d.mu.Lock()
	for d.running >= d.concurrency {
		d.cond.Wait()
	}
	d.running++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.running--
		d.mu.Unlock()
		d.cond.Signal()
	}()
	briefPB := &model_pb.SubmissionBriefReport{
		Status: model_pb.SubmissionStatus_Running,
	}
	if notifyC != nil {
		go func() { notifyC <- &grader_pb.GradeReport{Brief: briefPB} }()
	}
	logger := GetGraderLogger(ctx).With(zap.Uint64("submissionId", submissionId))
	containerIdCh := make(chan string)
	containerStartCh := make(chan bool)
	go func() {
		containerId := <-containerIdCh
		m := &grader_pb.DockerGraderMetadata{
			SubmissionId: submissionId, ContainerId: containerId,
		}
		if containerId != "" {
			m.Created = true
			notifyC <- &grader_pb.GradeReport{
				DockerMetadata: m,
			}
			started := <-containerStartCh
			m.Started = started
			if started {
				notifyC <- &grader_pb.GradeReport{
					DockerMetadata: m,
				}
			}
		}
	}()
	internalError, exitCode, resultsJSONPath, err := d.runDocker(
		ctx, basePath, submissionId, submission, config, cpusetMems, containerIdCh, containerStartCh,
	)
	if err == context.Canceled {
		briefPB.Status = model_pb.SubmissionStatus_Cancelled
		notifyC <- &grader_pb.GradeReport{Brief: briefPB}
		close(notifyC)
		return
	}
	logger.Debug(
		"Docker.Run.Returned", zap.Int64("internalError", internalError), zap.Int64("exitCode", exitCode),
		zap.String("resultsJSONPath", resultsJSONPath),
	)
	resultsPB := &model_pb.SubmissionReport{}
	var json []byte
	var resultsJSON *os.File
	if internalError == 0 {
		resultsJSON, err = os.Open(resultsJSONPath)
		if err != nil {
			internalError = ErrOpenResultJSON
			logger.Error("Result.Open", zap.Error(err))
			goto WriteReport
		}
		defer func(resultsJSON *os.File) {
			err := resultsJSON.Close()
			if err != nil {
				logger.Error("Result.Close", zap.Error(err))
			}
		}(resultsJSON)
		json, err = ioutil.ReadAll(resultsJSON)
		if err != nil {
			internalError = ErrReadResultJSON
			logger.Error("Result.ReadAll", zap.Error(err))
			goto WriteReport
		}
		err = protojson.Unmarshal(json, resultsPB)
		if err != nil {
			internalError = ErrUnmarshalResultJSON
			logger.Error("Result.Unmarshal", zap.Error(err))
			goto WriteReport
		}
		if resultsPB.GetScore() == 0 {
			score := uint64(0)
			maxScore := uint64(0)
			for _, testcase := range resultsPB.Tests {
				score += testcase.Score
				maxScore += testcase.MaxScore
				testcase.Output = truncateOutput(testcase.Output, 50*1024, "\n...truncated...\n")
			}
			resultsPB.Score = score
			resultsPB.MaxScore = maxScore
		}
	}
WriteReport:
	resultsPB.InternalError = internalError
	resultsPB.ExitCode = exitCode
	briefPB = &model_pb.SubmissionBriefReport{
		Score:         resultsPB.Score,
		MaxScore:      resultsPB.MaxScore,
		ExitCode:      resultsPB.ExitCode,
		InternalError: resultsPB.InternalError,
		Status:        model_pb.SubmissionStatus_Finished,
	}
	if internalError != 0 {
		briefPB.Status = model_pb.SubmissionStatus_Failed
	}
	if notifyC != nil {
		notifyC <- &grader_pb.GradeReport{
			Report: resultsPB,
			Brief:  briefPB,
		}
		close(notifyC)
	}
}

func NewDockerProgrammingGrader(concurrency int) *DockerProgrammingGrader {
	mu := &sync.Mutex{}
	cond := sync.NewCond(mu)
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(err)
	}
	return &DockerProgrammingGrader{cli: cli, mu: mu, cond: cond, concurrency: concurrency}
}

func NewHubGrader(svc *autograder_grpc.GraderHubService) *HubGrader {
	return &HubGrader{hubService: svc}
}
