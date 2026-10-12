package grpc

import (
	"context"

	autograder_pb "autograder-server/pkg/api/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (a *AutograderService) GetAllGraders(
	ctx context.Context, request *autograder_pb.GetAllGradersRequest,
) (*autograder_pb.GetAllGradersResponse, error) {
	return a.graderHubSvc.GetAllGraders(ctx)
}

func (a *AutograderService) StreamLog(
	request *autograder_pb.WebStreamLogRequest,
	server autograder_pb.AutograderService_StreamLogServer,
) error {
	ctx, err := a.AuthFunc(server.Context(), request, autograder_pb.AutograderService_StreamLog_FullMethodName)
	if err != nil {
		return err
	}
	ch, err := a.graderHubSvc.StreamLog(ctx, request.GetSubmissionId())
	if err != nil {
		return status.Error(codes.Internal, CodeStreamLog)
	}
	for data := range ch {
		err := server.SendMsg(&autograder_pb.WebStreamLogResponse{Data: data})
		if err != nil {
			break
		}
	}
	return nil
}
