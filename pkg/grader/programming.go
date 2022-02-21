package grader

import (
	model_pb "autograder-server/pkg/model/proto"
	"autograder-server/pkg/repository"
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/protobuf/encoding/protojson"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
)

type ProgrammingGrader interface {
	PullImage(image string) error
	GradeSubmission(submissionId uint64, submission *model_pb.Submission, config *model_pb.ProgrammingAssignmentConfig, notifyC chan *GradeFinished)
}

type DockerProgrammingGrader struct {
	cli         *client.Client
	srr         repository.SubmissionReportRepository
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

func (d *DockerProgrammingGrader) PullImage(image string) error {
	closer, err := d.cli.ImagePull(context.Background(), image, types.ImagePullOptions{})
	if err != nil {
		return err
	}
	_, err = ioutil.ReadAll(closer)
	return err
}

func (d *DockerProgrammingGrader) runDocker(ctx context.Context, submissionId uint64, submission *model_pb.Submission, config *model_pb.ProgrammingAssignmentConfig) (internalError int64, exitCode int64, resultsJSONPath string) {
	imgs, err := d.cli.ImageList(ctx, types.ImageListOptions{
		All:     false,
		Filters: filters.NewArgs(filters.Arg("reference", fmt.Sprintf("%s", config.Image))),
	})
	if err != nil {
		internalError = -1
		grpclog.Errorf("failed to list image for %d: %v", submissionId, err)
		return
	}
	if len(imgs) == 0 {
		closer, err := d.cli.ImagePull(ctx, config.Image, types.ImagePullOptions{})
		if err != nil {
			internalError = 1
			grpclog.Errorf("failed to pull image for %d: %v", submissionId, err)
			return
		}
		_, err = ioutil.ReadAll(closer)
		if err != nil {
			internalError = 2
			return
		}
	}
	ctCfg := &container.Config{
		Hostname:     "",
		Domainname:   "",
		User:         "",
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		OpenStdin:    true,
		StdinOnce:    false,
		Env:          nil,
		Entrypoint:   []string{"sh", "-c", "/autograder/run > /autograder/results/stdout 2> /autograder/results/stderr"},
		Image:        config.Image,
		Volumes:      nil,
		WorkingDir:   "/autograder",
	}
	cwd, _ := os.Getwd()
	runDir := filepath.Join(cwd, fmt.Sprintf("runs/submissions/%d", submissionId))
	resultsDir := filepath.Join(runDir, "results")
	resultsJSONPath = filepath.Join(resultsDir, "results.json")
	subDir := filepath.Join(cwd, submission.Path)
	os.RemoveAll(runDir)
	os.MkdirAll(runDir, 0755)
	os.MkdirAll(resultsDir, 0755)
	hstCfg := &container.HostConfig{
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: subDir, Target: "/autograder/submission"},
			{Type: mount.TypeBind, Source: resultsDir, Target: "/autograder/results"},
		},
	}
	netCfg := &network.NetworkingConfig{}
	platform := &specs.Platform{Architecture: "amd64", OS: "linux"}
	body, err := d.cli.ContainerCreate(ctx, ctCfg, hstCfg, netCfg, platform, "")
	if err != nil {
		internalError = 3
		grpclog.Errorf("failed to create container for %d: %v", submissionId, err)
		return
	}
	id := body.ID
	doneC, errC := d.cli.ContainerWait(ctx, id, container.WaitConditionNextExit)
	err = d.cli.ContainerStart(ctx, id, types.ContainerStartOptions{})
	if err != nil {
		internalError = 4
		grpclog.Errorf("failed to start container for %d: %v", submissionId, err)
		return
	}
	select {
	case err := <-errC:
		internalError = 5
		grpclog.Errorf("failed to wait container for %d: %v", submissionId, err)
		return
	case <-doneC:

	}
	containerDetail, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		internalError = 6
		grpclog.Errorf("failed to inspect container for %d: %v", submissionId, err)
		return
	}
	exitCode = int64(containerDetail.State.ExitCode)
	err = d.cli.ContainerRemove(ctx, id, types.ContainerRemoveOptions{})
	if err != nil {
		internalError = 7
		grpclog.Errorf("failed to remove container for %d: %v", submissionId, err)
		return
	}
	return
}

type GradeFinished struct {
	Report      *model_pb.SubmissionReport
	BriefReport *model_pb.SubmissionBriefReport
}

func (d *DockerProgrammingGrader) GradeSubmission(submissionId uint64, submission *model_pb.Submission, config *model_pb.ProgrammingAssignmentConfig, notifyC chan *GradeFinished) {
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
	ctx := context.Background()
	_ = d.srr.UpdateSubmissionBriefReport(ctx, submissionId, briefPB)
	internalError, exitCode, resultsJSONPath := d.runDocker(ctx, submissionId, submission, config)
	resultsPB := &model_pb.SubmissionReport{}
	var json []byte
	var err error
	var resultsJSON *os.File
	if internalError == 0 && exitCode == 0 {
		resultsJSON, err = os.Open(resultsJSONPath)
		if err != nil {
			internalError = 8
			goto WriteReport
		}
		defer resultsJSON.Close()
		json, err = ioutil.ReadAll(resultsJSON)
		if err != nil {
			internalError = 9
			goto WriteReport
		}
		err = protojson.Unmarshal(json, resultsPB)
		if err != nil {
			internalError = 10
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
	err = d.srr.UpdateSubmissionReport(ctx, submissionId, resultsPB)
	if err != nil {
		grpclog.Errorf("failed to update submission report for %d: %v", submissionId, err)
	}
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
	err = d.srr.UpdateSubmissionBriefReport(ctx, submissionId, briefPB)
	if err != nil {
		grpclog.Errorf("failed to update submission brief report for %d: %v", submissionId, err)
	}
	_ = d.srr.DeleteUnfinishedSubmission(ctx, submissionId)
	if notifyC != nil {
		notifyC <- &GradeFinished{
			Report:      resultsPB,
			BriefReport: briefPB,
		}
	}
}

func NewDockerProgrammingGrader(srr repository.SubmissionReportRepository) ProgrammingGrader {
	mu := &sync.Mutex{}
	cond := sync.NewCond(mu)
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		panic(err)
	}
	return &DockerProgrammingGrader{cli: cli, srr: srr, mu: mu, cond: cond, concurrency: 10}
}
