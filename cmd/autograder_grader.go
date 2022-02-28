package main

import (
	"context"
	"time"

	"autograder-server/pkg/grader"
	grader_pb "autograder-server/pkg/grader/proto"
	model_pb "autograder-server/pkg/model/proto"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func gradeOneSubmission(
	grader grader.ProgrammingGrader, req *grader_pb.GradeRequest, client grader_pb.GraderHubServiceClient,
) {
	ctx := context.Background()
	notifyC := make(chan *grader_pb.GradeReport)
	for {
		rpCli, err := client.GradeCallback(ctx)
		if err != nil {
			zap.L().Error("Grader.StartGradeCallback", zap.Error(err))
			time.Sleep(1 * time.Second)
			continue
		}
		go grader.GradeSubmission(req.GetSubmissionId(), req.GetSubmission(), req.GetConfig(), notifyC)
		for r := range notifyC {
			zap.L().Debug("Grader.ProgressReport", zap.Stringer("report", r))
			err := rpCli.Send(&grader_pb.GradeResponse{SubmissionId: req.SubmissionId, Report: r})
			if err != nil {
				zap.L().Error("Grader.SendResponse", zap.Error(err), zap.Uint64("submissionId", req.SubmissionId))
			}
		}
		break
	}
}

func main() {
	l, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(l)
	var graderId uint64
	conn, err := grpc.Dial("localhost:9999", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	client := grader_pb.NewGraderHubServiceClient(conn)
	registerRequest := &grader_pb.RegisterGraderRequest{
		Token: "",
		Info: &model_pb.GraderInfo{
			Hostname:    "localhost",
			Tags:        []string{"docker"},
			Concurrency: 5,
		},
	}
	dockerGrader := grader.NewDockerProgrammingGrader(5)
	resp, err := client.RegisterGrader(context.Background(), registerRequest)
	if err != nil {
		panic(err)
	}
	graderId = resp.GetGraderId()
	zap.L().Info("Grader.Registered", zap.Uint64("graderId", graderId))
	for {
		hbCtx, hbCancel := context.WithCancel(context.Background())
		hbCli, err := client.GraderHeartbeat(hbCtx)
		if err != nil {
			zap.L().Error("Grader.StartHeartbeat", zap.Error(err))
			time.Sleep(1 * time.Second)
			continue
		}
		// Heartbeat
		go func() {
			zap.L().Debug("Grader.StartHeartbeat")
			timer := time.NewTimer(10 * time.Second)
			for {
				zap.L().Debug("Grader.Heartbeat")
				err := hbCli.Send(&grader_pb.GraderHeartbeatRequest{Time: timestamppb.Now(), GraderId: graderId})
				if err != nil {
					zap.L().Error("Grader.Heartbeat", zap.Error(err))
					hbCancel()
					timer.Stop()
					break
				}
				select {
				case <-timer.C:
					timer.Reset(10 * time.Second)
				case <-hbCtx.Done():
					timer.Stop()
					break
				}
			}
		}()

		// Receive Grade Request
		for {
			request, err := hbCli.Recv()
			zap.L().Debug("Grader.Recv")
			if err != nil {
				zap.L().Error("Grader.Recv", zap.Error(err))
				hbCancel()
				break
			}
			gradeReqs := request.GetRequests()
			for _, req := range gradeReqs {
				zap.L().Debug("Grader.BeginGrade", zap.Uint64("submissionId", req.GetSubmissionId()))
				go gradeOneSubmission(dockerGrader, req, client)
			}
		}
	}
}
