// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.2.0
// - protoc             v3.19.2
// source: proto/api.proto

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

// AutograderServiceServer is the server API for AutograderService service.
// All implementations must embed UnimplementedAutograderServiceServer
// for forward compatibility
type AutograderServiceServer interface {
	Login(context.Context, *LoginRequest) (*LoginResponse, error)
	GetCourseList(context.Context, *GetCourseListRequest) (*GetCourseListResponse, error)
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
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "proto/api.proto",
}
