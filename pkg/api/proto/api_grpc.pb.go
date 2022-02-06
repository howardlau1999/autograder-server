// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.2.0
// - protoc             v3.19.2
// source: api.proto

package proto

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// AutograderServiceClient is the client API for AutograderService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type AutograderServiceClient interface {
	Login(ctx context.Context, in *LoginRequest, opts ...grpc.CallOption) (*LoginResponse, error)
	GetCourseList(ctx context.Context, in *GetCourseListRequest, opts ...grpc.CallOption) (*GetCourseListResponse, error)
	GetAssignmentsInCourse(ctx context.Context, in *GetAssignmentsInCourseRequest, opts ...grpc.CallOption) (*GetAssignmentsInCourseResponse, error)
	GetSubmissionsInAssignment(ctx context.Context, in *GetSubmissionsInAssignmentRequest, opts ...grpc.CallOption) (*GetSubmissionsInAssignmentResponse, error)
	SubscribeSubmissions(ctx context.Context, in *SubscribeSubmissionsRequest, opts ...grpc.CallOption) (AutograderService_SubscribeSubmissionsClient, error)
	SubscribeSubmission(ctx context.Context, in *SubscribeSubmissionRequest, opts ...grpc.CallOption) (AutograderService_SubscribeSubmissionClient, error)
	StreamSubmissionLog(ctx context.Context, in *StreamSubmissionLogRequest, opts ...grpc.CallOption) (AutograderService_StreamSubmissionLogClient, error)
	GetFile(ctx context.Context, in *GetFileRequest, opts ...grpc.CallOption) (AutograderService_GetFileClient, error)
	CreateManifest(ctx context.Context, in *CreateManifestRequest, opts ...grpc.CallOption) (*CreateManifestResponse, error)
	CreateSubmission(ctx context.Context, in *CreateSubmissionRequest, opts ...grpc.CallOption) (*CreateSubmissionResponse, error)
	InitUpload(ctx context.Context, in *InitUploadRequest, opts ...grpc.CallOption) (*InitUploadResponse, error)
	GetSubmissionReport(ctx context.Context, in *GetSubmissionReportRequest, opts ...grpc.CallOption) (*GetSubmissionReportResponse, error)
	GetAssignment(ctx context.Context, in *GetAssignmentRequest, opts ...grpc.CallOption) (*GetAssignmentResponse, error)
	GetCourse(ctx context.Context, in *GetCourseRequest, opts ...grpc.CallOption) (*GetCourseResponse, error)
	GetFilesInSubmission(ctx context.Context, in *GetFilesInSubmissionRequest, opts ...grpc.CallOption) (*GetFilesInSubmissionResponse, error)
}

type autograderServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewAutograderServiceClient(cc grpc.ClientConnInterface) AutograderServiceClient {
	return &autograderServiceClient{cc}
}

func (c *autograderServiceClient) Login(ctx context.Context, in *LoginRequest, opts ...grpc.CallOption) (*LoginResponse, error) {
	out := new(LoginResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/Login", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) GetCourseList(ctx context.Context, in *GetCourseListRequest, opts ...grpc.CallOption) (*GetCourseListResponse, error) {
	out := new(GetCourseListResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/GetCourseList", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) GetAssignmentsInCourse(ctx context.Context, in *GetAssignmentsInCourseRequest, opts ...grpc.CallOption) (*GetAssignmentsInCourseResponse, error) {
	out := new(GetAssignmentsInCourseResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/GetAssignmentsInCourse", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) GetSubmissionsInAssignment(ctx context.Context, in *GetSubmissionsInAssignmentRequest, opts ...grpc.CallOption) (*GetSubmissionsInAssignmentResponse, error) {
	out := new(GetSubmissionsInAssignmentResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/GetSubmissionsInAssignment", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) SubscribeSubmissions(ctx context.Context, in *SubscribeSubmissionsRequest, opts ...grpc.CallOption) (AutograderService_SubscribeSubmissionsClient, error) {
	stream, err := c.cc.NewStream(ctx, &AutograderService_ServiceDesc.Streams[0], "/AutograderService/SubscribeSubmissions", opts...)
	if err != nil {
		return nil, err
	}
	x := &autograderServiceSubscribeSubmissionsClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type AutograderService_SubscribeSubmissionsClient interface {
	Recv() (*SubscribeSubmissionsResponse, error)
	grpc.ClientStream
}

type autograderServiceSubscribeSubmissionsClient struct {
	grpc.ClientStream
}

func (x *autograderServiceSubscribeSubmissionsClient) Recv() (*SubscribeSubmissionsResponse, error) {
	m := new(SubscribeSubmissionsResponse)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *autograderServiceClient) SubscribeSubmission(ctx context.Context, in *SubscribeSubmissionRequest, opts ...grpc.CallOption) (AutograderService_SubscribeSubmissionClient, error) {
	stream, err := c.cc.NewStream(ctx, &AutograderService_ServiceDesc.Streams[1], "/AutograderService/SubscribeSubmission", opts...)
	if err != nil {
		return nil, err
	}
	x := &autograderServiceSubscribeSubmissionClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type AutograderService_SubscribeSubmissionClient interface {
	Recv() (*SubscribeSubmissionResponse, error)
	grpc.ClientStream
}

type autograderServiceSubscribeSubmissionClient struct {
	grpc.ClientStream
}

func (x *autograderServiceSubscribeSubmissionClient) Recv() (*SubscribeSubmissionResponse, error) {
	m := new(SubscribeSubmissionResponse)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *autograderServiceClient) StreamSubmissionLog(ctx context.Context, in *StreamSubmissionLogRequest, opts ...grpc.CallOption) (AutograderService_StreamSubmissionLogClient, error) {
	stream, err := c.cc.NewStream(ctx, &AutograderService_ServiceDesc.Streams[2], "/AutograderService/StreamSubmissionLog", opts...)
	if err != nil {
		return nil, err
	}
	x := &autograderServiceStreamSubmissionLogClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type AutograderService_StreamSubmissionLogClient interface {
	Recv() (*ChunkResponse, error)
	grpc.ClientStream
}

type autograderServiceStreamSubmissionLogClient struct {
	grpc.ClientStream
}

func (x *autograderServiceStreamSubmissionLogClient) Recv() (*ChunkResponse, error) {
	m := new(ChunkResponse)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *autograderServiceClient) GetFile(ctx context.Context, in *GetFileRequest, opts ...grpc.CallOption) (AutograderService_GetFileClient, error) {
	stream, err := c.cc.NewStream(ctx, &AutograderService_ServiceDesc.Streams[3], "/AutograderService/GetFile", opts...)
	if err != nil {
		return nil, err
	}
	x := &autograderServiceGetFileClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type AutograderService_GetFileClient interface {
	Recv() (*ChunkResponse, error)
	grpc.ClientStream
}

type autograderServiceGetFileClient struct {
	grpc.ClientStream
}

func (x *autograderServiceGetFileClient) Recv() (*ChunkResponse, error) {
	m := new(ChunkResponse)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *autograderServiceClient) CreateManifest(ctx context.Context, in *CreateManifestRequest, opts ...grpc.CallOption) (*CreateManifestResponse, error) {
	out := new(CreateManifestResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/CreateManifest", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) CreateSubmission(ctx context.Context, in *CreateSubmissionRequest, opts ...grpc.CallOption) (*CreateSubmissionResponse, error) {
	out := new(CreateSubmissionResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/CreateSubmission", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) InitUpload(ctx context.Context, in *InitUploadRequest, opts ...grpc.CallOption) (*InitUploadResponse, error) {
	out := new(InitUploadResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/InitUpload", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) GetSubmissionReport(ctx context.Context, in *GetSubmissionReportRequest, opts ...grpc.CallOption) (*GetSubmissionReportResponse, error) {
	out := new(GetSubmissionReportResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/GetSubmissionReport", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) GetAssignment(ctx context.Context, in *GetAssignmentRequest, opts ...grpc.CallOption) (*GetAssignmentResponse, error) {
	out := new(GetAssignmentResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/GetAssignment", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) GetCourse(ctx context.Context, in *GetCourseRequest, opts ...grpc.CallOption) (*GetCourseResponse, error) {
	out := new(GetCourseResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/GetCourse", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *autograderServiceClient) GetFilesInSubmission(ctx context.Context, in *GetFilesInSubmissionRequest, opts ...grpc.CallOption) (*GetFilesInSubmissionResponse, error) {
	out := new(GetFilesInSubmissionResponse)
	err := c.cc.Invoke(ctx, "/AutograderService/GetFilesInSubmission", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AutograderServiceServer is the server API for AutograderService service.
// All implementations must embed UnimplementedAutograderServiceServer
// for forward compatibility
type AutograderServiceServer interface {
	Login(context.Context, *LoginRequest) (*LoginResponse, error)
	GetCourseList(context.Context, *GetCourseListRequest) (*GetCourseListResponse, error)
	GetAssignmentsInCourse(context.Context, *GetAssignmentsInCourseRequest) (*GetAssignmentsInCourseResponse, error)
	GetSubmissionsInAssignment(context.Context, *GetSubmissionsInAssignmentRequest) (*GetSubmissionsInAssignmentResponse, error)
	SubscribeSubmissions(*SubscribeSubmissionsRequest, AutograderService_SubscribeSubmissionsServer) error
	SubscribeSubmission(*SubscribeSubmissionRequest, AutograderService_SubscribeSubmissionServer) error
	StreamSubmissionLog(*StreamSubmissionLogRequest, AutograderService_StreamSubmissionLogServer) error
	GetFile(*GetFileRequest, AutograderService_GetFileServer) error
	CreateManifest(context.Context, *CreateManifestRequest) (*CreateManifestResponse, error)
	CreateSubmission(context.Context, *CreateSubmissionRequest) (*CreateSubmissionResponse, error)
	InitUpload(context.Context, *InitUploadRequest) (*InitUploadResponse, error)
	GetSubmissionReport(context.Context, *GetSubmissionReportRequest) (*GetSubmissionReportResponse, error)
	GetAssignment(context.Context, *GetAssignmentRequest) (*GetAssignmentResponse, error)
	GetCourse(context.Context, *GetCourseRequest) (*GetCourseResponse, error)
	GetFilesInSubmission(context.Context, *GetFilesInSubmissionRequest) (*GetFilesInSubmissionResponse, error)
	mustEmbedUnimplementedAutograderServiceServer()
}

// UnimplementedAutograderServiceServer must be embedded to have forward compatible implementations.
type UnimplementedAutograderServiceServer struct {
}

func (UnimplementedAutograderServiceServer) Login(context.Context, *LoginRequest) (*LoginResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Login not implemented")
}
func (UnimplementedAutograderServiceServer) GetCourseList(context.Context, *GetCourseListRequest) (*GetCourseListResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetCourseList not implemented")
}
func (UnimplementedAutograderServiceServer) GetAssignmentsInCourse(context.Context, *GetAssignmentsInCourseRequest) (*GetAssignmentsInCourseResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetAssignmentsInCourse not implemented")
}
func (UnimplementedAutograderServiceServer) GetSubmissionsInAssignment(context.Context, *GetSubmissionsInAssignmentRequest) (*GetSubmissionsInAssignmentResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetSubmissionsInAssignment not implemented")
}
func (UnimplementedAutograderServiceServer) SubscribeSubmissions(*SubscribeSubmissionsRequest, AutograderService_SubscribeSubmissionsServer) error {
	return status.Errorf(codes.Unimplemented, "method SubscribeSubmissions not implemented")
}
func (UnimplementedAutograderServiceServer) SubscribeSubmission(*SubscribeSubmissionRequest, AutograderService_SubscribeSubmissionServer) error {
	return status.Errorf(codes.Unimplemented, "method SubscribeSubmission not implemented")
}
func (UnimplementedAutograderServiceServer) StreamSubmissionLog(*StreamSubmissionLogRequest, AutograderService_StreamSubmissionLogServer) error {
	return status.Errorf(codes.Unimplemented, "method StreamSubmissionLog not implemented")
}
func (UnimplementedAutograderServiceServer) GetFile(*GetFileRequest, AutograderService_GetFileServer) error {
	return status.Errorf(codes.Unimplemented, "method GetFile not implemented")
}
func (UnimplementedAutograderServiceServer) CreateManifest(context.Context, *CreateManifestRequest) (*CreateManifestResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateManifest not implemented")
}
func (UnimplementedAutograderServiceServer) CreateSubmission(context.Context, *CreateSubmissionRequest) (*CreateSubmissionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateSubmission not implemented")
}
func (UnimplementedAutograderServiceServer) InitUpload(context.Context, *InitUploadRequest) (*InitUploadResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method InitUpload not implemented")
}
func (UnimplementedAutograderServiceServer) GetSubmissionReport(context.Context, *GetSubmissionReportRequest) (*GetSubmissionReportResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetSubmissionReport not implemented")
}
func (UnimplementedAutograderServiceServer) GetAssignment(context.Context, *GetAssignmentRequest) (*GetAssignmentResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetAssignment not implemented")
}
func (UnimplementedAutograderServiceServer) GetCourse(context.Context, *GetCourseRequest) (*GetCourseResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetCourse not implemented")
}
func (UnimplementedAutograderServiceServer) GetFilesInSubmission(context.Context, *GetFilesInSubmissionRequest) (*GetFilesInSubmissionResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetFilesInSubmission not implemented")
}
func (UnimplementedAutograderServiceServer) mustEmbedUnimplementedAutograderServiceServer() {}

// UnsafeAutograderServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to AutograderServiceServer will
// result in compilation errors.
type UnsafeAutograderServiceServer interface {
	mustEmbedUnimplementedAutograderServiceServer()
}

func RegisterAutograderServiceServer(s grpc.ServiceRegistrar, srv AutograderServiceServer) {
	s.RegisterService(&AutograderService_ServiceDesc, srv)
}

func _AutograderService_Login_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(LoginRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).Login(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/Login",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).Login(ctx, req.(*LoginRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_GetCourseList_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetCourseListRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).GetCourseList(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/GetCourseList",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).GetCourseList(ctx, req.(*GetCourseListRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_GetAssignmentsInCourse_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetAssignmentsInCourseRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).GetAssignmentsInCourse(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/GetAssignmentsInCourse",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).GetAssignmentsInCourse(ctx, req.(*GetAssignmentsInCourseRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_GetSubmissionsInAssignment_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetSubmissionsInAssignmentRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).GetSubmissionsInAssignment(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/GetSubmissionsInAssignment",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).GetSubmissionsInAssignment(ctx, req.(*GetSubmissionsInAssignmentRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_SubscribeSubmissions_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(SubscribeSubmissionsRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(AutograderServiceServer).SubscribeSubmissions(m, &autograderServiceSubscribeSubmissionsServer{stream})
}

type AutograderService_SubscribeSubmissionsServer interface {
	Send(*SubscribeSubmissionsResponse) error
	grpc.ServerStream
}

type autograderServiceSubscribeSubmissionsServer struct {
	grpc.ServerStream
}

func (x *autograderServiceSubscribeSubmissionsServer) Send(m *SubscribeSubmissionsResponse) error {
	return x.ServerStream.SendMsg(m)
}

func _AutograderService_SubscribeSubmission_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(SubscribeSubmissionRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(AutograderServiceServer).SubscribeSubmission(m, &autograderServiceSubscribeSubmissionServer{stream})
}

type AutograderService_SubscribeSubmissionServer interface {
	Send(*SubscribeSubmissionResponse) error
	grpc.ServerStream
}

type autograderServiceSubscribeSubmissionServer struct {
	grpc.ServerStream
}

func (x *autograderServiceSubscribeSubmissionServer) Send(m *SubscribeSubmissionResponse) error {
	return x.ServerStream.SendMsg(m)
}

func _AutograderService_StreamSubmissionLog_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(StreamSubmissionLogRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(AutograderServiceServer).StreamSubmissionLog(m, &autograderServiceStreamSubmissionLogServer{stream})
}

type AutograderService_StreamSubmissionLogServer interface {
	Send(*ChunkResponse) error
	grpc.ServerStream
}

type autograderServiceStreamSubmissionLogServer struct {
	grpc.ServerStream
}

func (x *autograderServiceStreamSubmissionLogServer) Send(m *ChunkResponse) error {
	return x.ServerStream.SendMsg(m)
}

func _AutograderService_GetFile_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(GetFileRequest)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(AutograderServiceServer).GetFile(m, &autograderServiceGetFileServer{stream})
}

type AutograderService_GetFileServer interface {
	Send(*ChunkResponse) error
	grpc.ServerStream
}

type autograderServiceGetFileServer struct {
	grpc.ServerStream
}

func (x *autograderServiceGetFileServer) Send(m *ChunkResponse) error {
	return x.ServerStream.SendMsg(m)
}

func _AutograderService_CreateManifest_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateManifestRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).CreateManifest(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/CreateManifest",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).CreateManifest(ctx, req.(*CreateManifestRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_CreateSubmission_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(CreateSubmissionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).CreateSubmission(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/CreateSubmission",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).CreateSubmission(ctx, req.(*CreateSubmissionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_InitUpload_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(InitUploadRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).InitUpload(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/InitUpload",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).InitUpload(ctx, req.(*InitUploadRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_GetSubmissionReport_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetSubmissionReportRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).GetSubmissionReport(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/GetSubmissionReport",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).GetSubmissionReport(ctx, req.(*GetSubmissionReportRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_GetAssignment_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetAssignmentRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).GetAssignment(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/GetAssignment",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).GetAssignment(ctx, req.(*GetAssignmentRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_GetCourse_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetCourseRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).GetCourse(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/GetCourse",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).GetCourse(ctx, req.(*GetCourseRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _AutograderService_GetFilesInSubmission_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetFilesInSubmissionRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AutograderServiceServer).GetFilesInSubmission(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/AutograderService/GetFilesInSubmission",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(AutograderServiceServer).GetFilesInSubmission(ctx, req.(*GetFilesInSubmissionRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// AutograderService_ServiceDesc is the grpc.ServiceDesc for AutograderService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var AutograderService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "AutograderService",
	HandlerType: (*AutograderServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Login",
			Handler:    _AutograderService_Login_Handler,
		},
		{
			MethodName: "GetCourseList",
			Handler:    _AutograderService_GetCourseList_Handler,
		},
		{
			MethodName: "GetAssignmentsInCourse",
			Handler:    _AutograderService_GetAssignmentsInCourse_Handler,
		},
		{
			MethodName: "GetSubmissionsInAssignment",
			Handler:    _AutograderService_GetSubmissionsInAssignment_Handler,
		},
		{
			MethodName: "CreateManifest",
			Handler:    _AutograderService_CreateManifest_Handler,
		},
		{
			MethodName: "CreateSubmission",
			Handler:    _AutograderService_CreateSubmission_Handler,
		},
		{
			MethodName: "InitUpload",
			Handler:    _AutograderService_InitUpload_Handler,
		},
		{
			MethodName: "GetSubmissionReport",
			Handler:    _AutograderService_GetSubmissionReport_Handler,
		},
		{
			MethodName: "GetAssignment",
			Handler:    _AutograderService_GetAssignment_Handler,
		},
		{
			MethodName: "GetCourse",
			Handler:    _AutograderService_GetCourse_Handler,
		},
		{
			MethodName: "GetFilesInSubmission",
			Handler:    _AutograderService_GetFilesInSubmission_Handler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "SubscribeSubmissions",
			Handler:       _AutograderService_SubscribeSubmissions_Handler,
			ServerStreams: true,
		},
		{
			StreamName:    "SubscribeSubmission",
			Handler:       _AutograderService_SubscribeSubmission_Handler,
			ServerStreams: true,
		},
		{
			StreamName:    "StreamSubmissionLog",
			Handler:       _AutograderService_StreamSubmissionLog_Handler,
			ServerStreams: true,
		},
		{
			StreamName:    "GetFile",
			Handler:       _AutograderService_GetFile_Handler,
			ServerStreams: true,
		},
	},
	Metadata: "api.proto",
}
