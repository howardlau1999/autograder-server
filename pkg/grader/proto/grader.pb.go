// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.27.1
// 	protoc        v3.19.4
// source: grader.proto

package proto

import (
	proto "autograder-server/pkg/model/proto"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type RegisterGraderRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Token string            `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	Info  *proto.GraderInfo `protobuf:"bytes,2,opt,name=info,proto3" json:"info,omitempty"`
}

func (x *RegisterGraderRequest) Reset() {
	*x = RegisterGraderRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_grader_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *RegisterGraderRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RegisterGraderRequest) ProtoMessage() {}

func (x *RegisterGraderRequest) ProtoReflect() protoreflect.Message {
	mi := &file_grader_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RegisterGraderRequest.ProtoReflect.Descriptor instead.
func (*RegisterGraderRequest) Descriptor() ([]byte, []int) {
	return file_grader_proto_rawDescGZIP(), []int{0}
}

func (x *RegisterGraderRequest) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

func (x *RegisterGraderRequest) GetInfo() *proto.GraderInfo {
	if x != nil {
		return x.Info
	}
	return nil
}

type RegisterGraderResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	GraderId uint64 `protobuf:"varint,1,opt,name=grader_id,json=graderId,proto3" json:"grader_id,omitempty"`
}

func (x *RegisterGraderResponse) Reset() {
	*x = RegisterGraderResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_grader_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *RegisterGraderResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RegisterGraderResponse) ProtoMessage() {}

func (x *RegisterGraderResponse) ProtoReflect() protoreflect.Message {
	mi := &file_grader_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RegisterGraderResponse.ProtoReflect.Descriptor instead.
func (*RegisterGraderResponse) Descriptor() ([]byte, []int) {
	return file_grader_proto_rawDescGZIP(), []int{1}
}

func (x *RegisterGraderResponse) GetGraderId() uint64 {
	if x != nil {
		return x.GraderId
	}
	return 0
}

type GraderHeartbeatRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Time        *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=time,proto3" json:"time,omitempty"`
	GraderId    uint64                 `protobuf:"varint,2,opt,name=grader_id,json=graderId,proto3" json:"grader_id,omitempty"`
	Concurrency uint64                 `protobuf:"varint,3,opt,name=concurrency,proto3" json:"concurrency,omitempty"`
}

func (x *GraderHeartbeatRequest) Reset() {
	*x = GraderHeartbeatRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_grader_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GraderHeartbeatRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GraderHeartbeatRequest) ProtoMessage() {}

func (x *GraderHeartbeatRequest) ProtoReflect() protoreflect.Message {
	mi := &file_grader_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GraderHeartbeatRequest.ProtoReflect.Descriptor instead.
func (*GraderHeartbeatRequest) Descriptor() ([]byte, []int) {
	return file_grader_proto_rawDescGZIP(), []int{2}
}

func (x *GraderHeartbeatRequest) GetTime() *timestamppb.Timestamp {
	if x != nil {
		return x.Time
	}
	return nil
}

func (x *GraderHeartbeatRequest) GetGraderId() uint64 {
	if x != nil {
		return x.GraderId
	}
	return 0
}

func (x *GraderHeartbeatRequest) GetConcurrency() uint64 {
	if x != nil {
		return x.Concurrency
	}
	return 0
}

type GraderHeartbeatResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Requests []*GradeRequest `protobuf:"bytes,1,rep,name=requests,proto3" json:"requests,omitempty"`
}

func (x *GraderHeartbeatResponse) Reset() {
	*x = GraderHeartbeatResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_grader_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GraderHeartbeatResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GraderHeartbeatResponse) ProtoMessage() {}

func (x *GraderHeartbeatResponse) ProtoReflect() protoreflect.Message {
	mi := &file_grader_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GraderHeartbeatResponse.ProtoReflect.Descriptor instead.
func (*GraderHeartbeatResponse) Descriptor() ([]byte, []int) {
	return file_grader_proto_rawDescGZIP(), []int{3}
}

func (x *GraderHeartbeatResponse) GetRequests() []*GradeRequest {
	if x != nil {
		return x.Requests
	}
	return nil
}

type GradeRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	SubmissionId uint64                             `protobuf:"varint,1,opt,name=submission_id,json=submissionId,proto3" json:"submission_id,omitempty"`
	Submission   *proto.Submission                  `protobuf:"bytes,2,opt,name=submission,proto3" json:"submission,omitempty"`
	Config       *proto.ProgrammingAssignmentConfig `protobuf:"bytes,3,opt,name=config,proto3" json:"config,omitempty"`
}

func (x *GradeRequest) Reset() {
	*x = GradeRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_grader_proto_msgTypes[4]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GradeRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GradeRequest) ProtoMessage() {}

func (x *GradeRequest) ProtoReflect() protoreflect.Message {
	mi := &file_grader_proto_msgTypes[4]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GradeRequest.ProtoReflect.Descriptor instead.
func (*GradeRequest) Descriptor() ([]byte, []int) {
	return file_grader_proto_rawDescGZIP(), []int{4}
}

func (x *GradeRequest) GetSubmissionId() uint64 {
	if x != nil {
		return x.SubmissionId
	}
	return 0
}

func (x *GradeRequest) GetSubmission() *proto.Submission {
	if x != nil {
		return x.Submission
	}
	return nil
}

func (x *GradeRequest) GetConfig() *proto.ProgrammingAssignmentConfig {
	if x != nil {
		return x.Config
	}
	return nil
}

type GradeReport struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Report *proto.SubmissionReport      `protobuf:"bytes,1,opt,name=report,proto3" json:"report,omitempty"`
	Brief  *proto.SubmissionBriefReport `protobuf:"bytes,2,opt,name=brief,proto3" json:"brief,omitempty"`
}

func (x *GradeReport) Reset() {
	*x = GradeReport{}
	if protoimpl.UnsafeEnabled {
		mi := &file_grader_proto_msgTypes[5]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GradeReport) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GradeReport) ProtoMessage() {}

func (x *GradeReport) ProtoReflect() protoreflect.Message {
	mi := &file_grader_proto_msgTypes[5]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GradeReport.ProtoReflect.Descriptor instead.
func (*GradeReport) Descriptor() ([]byte, []int) {
	return file_grader_proto_rawDescGZIP(), []int{5}
}

func (x *GradeReport) GetReport() *proto.SubmissionReport {
	if x != nil {
		return x.Report
	}
	return nil
}

func (x *GradeReport) GetBrief() *proto.SubmissionBriefReport {
	if x != nil {
		return x.Brief
	}
	return nil
}

type GradeResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	SubmissionId uint64       `protobuf:"varint,1,opt,name=submission_id,json=submissionId,proto3" json:"submission_id,omitempty"`
	Report       *GradeReport `protobuf:"bytes,2,opt,name=report,proto3" json:"report,omitempty"`
}

func (x *GradeResponse) Reset() {
	*x = GradeResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_grader_proto_msgTypes[6]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GradeResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GradeResponse) ProtoMessage() {}

func (x *GradeResponse) ProtoReflect() protoreflect.Message {
	mi := &file_grader_proto_msgTypes[6]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GradeResponse.ProtoReflect.Descriptor instead.
func (*GradeResponse) Descriptor() ([]byte, []int) {
	return file_grader_proto_rawDescGZIP(), []int{6}
}

func (x *GradeResponse) GetSubmissionId() uint64 {
	if x != nil {
		return x.SubmissionId
	}
	return 0
}

func (x *GradeResponse) GetReport() *GradeReport {
	if x != nil {
		return x.Report
	}
	return nil
}

type GradeCallbackResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields
}

func (x *GradeCallbackResponse) Reset() {
	*x = GradeCallbackResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_grader_proto_msgTypes[7]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *GradeCallbackResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GradeCallbackResponse) ProtoMessage() {}

func (x *GradeCallbackResponse) ProtoReflect() protoreflect.Message {
	mi := &file_grader_proto_msgTypes[7]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GradeCallbackResponse.ProtoReflect.Descriptor instead.
func (*GradeCallbackResponse) Descriptor() ([]byte, []int) {
	return file_grader_proto_rawDescGZIP(), []int{7}
}

var File_grader_proto protoreflect.FileDescriptor

var file_grader_proto_rawDesc = []byte{
	0x0a, 0x0c, 0x67, 0x72, 0x61, 0x64, 0x65, 0x72, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x1f,
	0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f,
	0x74, 0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a,
	0x0b, 0x6d, 0x6f, 0x64, 0x65, 0x6c, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x4e, 0x0a, 0x15,
	0x52, 0x65, 0x67, 0x69, 0x73, 0x74, 0x65, 0x72, 0x47, 0x72, 0x61, 0x64, 0x65, 0x72, 0x52, 0x65,
	0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x14, 0x0a, 0x05, 0x74, 0x6f, 0x6b, 0x65, 0x6e, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x05, 0x74, 0x6f, 0x6b, 0x65, 0x6e, 0x12, 0x1f, 0x0a, 0x04, 0x69,
	0x6e, 0x66, 0x6f, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x0b, 0x2e, 0x47, 0x72, 0x61, 0x64,
	0x65, 0x72, 0x49, 0x6e, 0x66, 0x6f, 0x52, 0x04, 0x69, 0x6e, 0x66, 0x6f, 0x22, 0x35, 0x0a, 0x16,
	0x52, 0x65, 0x67, 0x69, 0x73, 0x74, 0x65, 0x72, 0x47, 0x72, 0x61, 0x64, 0x65, 0x72, 0x52, 0x65,
	0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x1b, 0x0a, 0x09, 0x67, 0x72, 0x61, 0x64, 0x65, 0x72,
	0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x04, 0x52, 0x08, 0x67, 0x72, 0x61, 0x64, 0x65,
	0x72, 0x49, 0x64, 0x22, 0x87, 0x01, 0x0a, 0x16, 0x47, 0x72, 0x61, 0x64, 0x65, 0x72, 0x48, 0x65,
	0x61, 0x72, 0x74, 0x62, 0x65, 0x61, 0x74, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x2e,
	0x0a, 0x04, 0x74, 0x69, 0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x1a, 0x2e, 0x67,
	0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x54,
	0x69, 0x6d, 0x65, 0x73, 0x74, 0x61, 0x6d, 0x70, 0x52, 0x04, 0x74, 0x69, 0x6d, 0x65, 0x12, 0x1b,
	0x0a, 0x09, 0x67, 0x72, 0x61, 0x64, 0x65, 0x72, 0x5f, 0x69, 0x64, 0x18, 0x02, 0x20, 0x01, 0x28,
	0x04, 0x52, 0x08, 0x67, 0x72, 0x61, 0x64, 0x65, 0x72, 0x49, 0x64, 0x12, 0x20, 0x0a, 0x0b, 0x63,
	0x6f, 0x6e, 0x63, 0x75, 0x72, 0x72, 0x65, 0x6e, 0x63, 0x79, 0x18, 0x03, 0x20, 0x01, 0x28, 0x04,
	0x52, 0x0b, 0x63, 0x6f, 0x6e, 0x63, 0x75, 0x72, 0x72, 0x65, 0x6e, 0x63, 0x79, 0x22, 0x44, 0x0a,
	0x17, 0x47, 0x72, 0x61, 0x64, 0x65, 0x72, 0x48, 0x65, 0x61, 0x72, 0x74, 0x62, 0x65, 0x61, 0x74,
	0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x29, 0x0a, 0x08, 0x72, 0x65, 0x71, 0x75,
	0x65, 0x73, 0x74, 0x73, 0x18, 0x01, 0x20, 0x03, 0x28, 0x0b, 0x32, 0x0d, 0x2e, 0x47, 0x72, 0x61,
	0x64, 0x65, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x52, 0x08, 0x72, 0x65, 0x71, 0x75, 0x65,
	0x73, 0x74, 0x73, 0x22, 0x96, 0x01, 0x0a, 0x0c, 0x47, 0x72, 0x61, 0x64, 0x65, 0x52, 0x65, 0x71,
	0x75, 0x65, 0x73, 0x74, 0x12, 0x23, 0x0a, 0x0d, 0x73, 0x75, 0x62, 0x6d, 0x69, 0x73, 0x73, 0x69,
	0x6f, 0x6e, 0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x04, 0x52, 0x0c, 0x73, 0x75, 0x62,
	0x6d, 0x69, 0x73, 0x73, 0x69, 0x6f, 0x6e, 0x49, 0x64, 0x12, 0x2b, 0x0a, 0x0a, 0x73, 0x75, 0x62,
	0x6d, 0x69, 0x73, 0x73, 0x69, 0x6f, 0x6e, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x0b, 0x2e,
	0x53, 0x75, 0x62, 0x6d, 0x69, 0x73, 0x73, 0x69, 0x6f, 0x6e, 0x52, 0x0a, 0x73, 0x75, 0x62, 0x6d,
	0x69, 0x73, 0x73, 0x69, 0x6f, 0x6e, 0x12, 0x34, 0x0a, 0x06, 0x63, 0x6f, 0x6e, 0x66, 0x69, 0x67,
	0x18, 0x03, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x1c, 0x2e, 0x50, 0x72, 0x6f, 0x67, 0x72, 0x61, 0x6d,
	0x6d, 0x69, 0x6e, 0x67, 0x41, 0x73, 0x73, 0x69, 0x67, 0x6e, 0x6d, 0x65, 0x6e, 0x74, 0x43, 0x6f,
	0x6e, 0x66, 0x69, 0x67, 0x52, 0x06, 0x63, 0x6f, 0x6e, 0x66, 0x69, 0x67, 0x22, 0x66, 0x0a, 0x0b,
	0x47, 0x72, 0x61, 0x64, 0x65, 0x52, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x12, 0x29, 0x0a, 0x06, 0x72,
	0x65, 0x70, 0x6f, 0x72, 0x74, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x11, 0x2e, 0x53, 0x75,
	0x62, 0x6d, 0x69, 0x73, 0x73, 0x69, 0x6f, 0x6e, 0x52, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x52, 0x06,
	0x72, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x12, 0x2c, 0x0a, 0x05, 0x62, 0x72, 0x69, 0x65, 0x66, 0x18,
	0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x16, 0x2e, 0x53, 0x75, 0x62, 0x6d, 0x69, 0x73, 0x73, 0x69,
	0x6f, 0x6e, 0x42, 0x72, 0x69, 0x65, 0x66, 0x52, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x52, 0x05, 0x62,
	0x72, 0x69, 0x65, 0x66, 0x22, 0x5a, 0x0a, 0x0d, 0x47, 0x72, 0x61, 0x64, 0x65, 0x52, 0x65, 0x73,
	0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x23, 0x0a, 0x0d, 0x73, 0x75, 0x62, 0x6d, 0x69, 0x73, 0x73,
	0x69, 0x6f, 0x6e, 0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x04, 0x52, 0x0c, 0x73, 0x75,
	0x62, 0x6d, 0x69, 0x73, 0x73, 0x69, 0x6f, 0x6e, 0x49, 0x64, 0x12, 0x24, 0x0a, 0x06, 0x72, 0x65,
	0x70, 0x6f, 0x72, 0x74, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x0c, 0x2e, 0x47, 0x72, 0x61,
	0x64, 0x65, 0x52, 0x65, 0x70, 0x6f, 0x72, 0x74, 0x52, 0x06, 0x72, 0x65, 0x70, 0x6f, 0x72, 0x74,
	0x22, 0x17, 0x0a, 0x15, 0x47, 0x72, 0x61, 0x64, 0x65, 0x43, 0x61, 0x6c, 0x6c, 0x62, 0x61, 0x63,
	0x6b, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x32, 0x8a, 0x02, 0x0a, 0x10, 0x47, 0x72,
	0x61, 0x64, 0x65, 0x72, 0x48, 0x75, 0x62, 0x53, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x12, 0x41,
	0x0a, 0x0e, 0x52, 0x65, 0x67, 0x69, 0x73, 0x74, 0x65, 0x72, 0x47, 0x72, 0x61, 0x64, 0x65, 0x72,
	0x12, 0x16, 0x2e, 0x52, 0x65, 0x67, 0x69, 0x73, 0x74, 0x65, 0x72, 0x47, 0x72, 0x61, 0x64, 0x65,
	0x72, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x17, 0x2e, 0x52, 0x65, 0x67, 0x69, 0x73,
	0x74, 0x65, 0x72, 0x47, 0x72, 0x61, 0x64, 0x65, 0x72, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73,
	0x65, 0x12, 0x2e, 0x0a, 0x05, 0x47, 0x72, 0x61, 0x64, 0x65, 0x12, 0x0d, 0x2e, 0x47, 0x72, 0x61,
	0x64, 0x65, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x16, 0x2e, 0x47, 0x72, 0x61, 0x64,
	0x65, 0x43, 0x61, 0x6c, 0x6c, 0x62, 0x61, 0x63, 0x6b, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73,
	0x65, 0x12, 0x48, 0x0a, 0x0f, 0x47, 0x72, 0x61, 0x64, 0x65, 0x72, 0x48, 0x65, 0x61, 0x72, 0x74,
	0x62, 0x65, 0x61, 0x74, 0x12, 0x17, 0x2e, 0x47, 0x72, 0x61, 0x64, 0x65, 0x72, 0x48, 0x65, 0x61,
	0x72, 0x74, 0x62, 0x65, 0x61, 0x74, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x18, 0x2e,
	0x47, 0x72, 0x61, 0x64, 0x65, 0x72, 0x48, 0x65, 0x61, 0x72, 0x74, 0x62, 0x65, 0x61, 0x74, 0x52,
	0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x28, 0x01, 0x30, 0x01, 0x12, 0x39, 0x0a, 0x0d, 0x47,
	0x72, 0x61, 0x64, 0x65, 0x43, 0x61, 0x6c, 0x6c, 0x62, 0x61, 0x63, 0x6b, 0x12, 0x0e, 0x2e, 0x47,
	0x72, 0x61, 0x64, 0x65, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x1a, 0x16, 0x2e, 0x47,
	0x72, 0x61, 0x64, 0x65, 0x43, 0x61, 0x6c, 0x6c, 0x62, 0x61, 0x63, 0x6b, 0x52, 0x65, 0x73, 0x70,
	0x6f, 0x6e, 0x73, 0x65, 0x28, 0x01, 0x42, 0x24, 0x5a, 0x22, 0x61, 0x75, 0x74, 0x6f, 0x67, 0x72,
	0x61, 0x64, 0x65, 0x72, 0x2d, 0x73, 0x65, 0x72, 0x76, 0x65, 0x72, 0x2f, 0x70, 0x6b, 0x67, 0x2f,
	0x67, 0x72, 0x61, 0x64, 0x65, 0x72, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x06, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_grader_proto_rawDescOnce sync.Once
	file_grader_proto_rawDescData = file_grader_proto_rawDesc
)

func file_grader_proto_rawDescGZIP() []byte {
	file_grader_proto_rawDescOnce.Do(func() {
		file_grader_proto_rawDescData = protoimpl.X.CompressGZIP(file_grader_proto_rawDescData)
	})
	return file_grader_proto_rawDescData
}

var file_grader_proto_msgTypes = make([]protoimpl.MessageInfo, 8)
var file_grader_proto_goTypes = []interface{}{
	(*RegisterGraderRequest)(nil),             // 0: RegisterGraderRequest
	(*RegisterGraderResponse)(nil),            // 1: RegisterGraderResponse
	(*GraderHeartbeatRequest)(nil),            // 2: GraderHeartbeatRequest
	(*GraderHeartbeatResponse)(nil),           // 3: GraderHeartbeatResponse
	(*GradeRequest)(nil),                      // 4: GradeRequest
	(*GradeReport)(nil),                       // 5: GradeReport
	(*GradeResponse)(nil),                     // 6: GradeResponse
	(*GradeCallbackResponse)(nil),             // 7: GradeCallbackResponse
	(*proto.GraderInfo)(nil),                  // 8: GraderInfo
	(*timestamppb.Timestamp)(nil),             // 9: google.protobuf.Timestamp
	(*proto.Submission)(nil),                  // 10: Submission
	(*proto.ProgrammingAssignmentConfig)(nil), // 11: ProgrammingAssignmentConfig
	(*proto.SubmissionReport)(nil),            // 12: SubmissionReport
	(*proto.SubmissionBriefReport)(nil),       // 13: SubmissionBriefReport
}
var file_grader_proto_depIdxs = []int32{
	8,  // 0: RegisterGraderRequest.info:type_name -> GraderInfo
	9,  // 1: GraderHeartbeatRequest.time:type_name -> google.protobuf.Timestamp
	4,  // 2: GraderHeartbeatResponse.requests:type_name -> GradeRequest
	10, // 3: GradeRequest.submission:type_name -> Submission
	11, // 4: GradeRequest.config:type_name -> ProgrammingAssignmentConfig
	12, // 5: GradeReport.report:type_name -> SubmissionReport
	13, // 6: GradeReport.brief:type_name -> SubmissionBriefReport
	5,  // 7: GradeResponse.report:type_name -> GradeReport
	0,  // 8: GraderHubService.RegisterGrader:input_type -> RegisterGraderRequest
	4,  // 9: GraderHubService.Grade:input_type -> GradeRequest
	2,  // 10: GraderHubService.GraderHeartbeat:input_type -> GraderHeartbeatRequest
	6,  // 11: GraderHubService.GradeCallback:input_type -> GradeResponse
	1,  // 12: GraderHubService.RegisterGrader:output_type -> RegisterGraderResponse
	7,  // 13: GraderHubService.Grade:output_type -> GradeCallbackResponse
	3,  // 14: GraderHubService.GraderHeartbeat:output_type -> GraderHeartbeatResponse
	7,  // 15: GraderHubService.GradeCallback:output_type -> GradeCallbackResponse
	12, // [12:16] is the sub-list for method output_type
	8,  // [8:12] is the sub-list for method input_type
	8,  // [8:8] is the sub-list for extension type_name
	8,  // [8:8] is the sub-list for extension extendee
	0,  // [0:8] is the sub-list for field type_name
}

func init() { file_grader_proto_init() }
func file_grader_proto_init() {
	if File_grader_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_grader_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*RegisterGraderRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_grader_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*RegisterGraderResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_grader_proto_msgTypes[2].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GraderHeartbeatRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_grader_proto_msgTypes[3].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GraderHeartbeatResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_grader_proto_msgTypes[4].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GradeRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_grader_proto_msgTypes[5].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GradeReport); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_grader_proto_msgTypes[6].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GradeResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_grader_proto_msgTypes[7].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*GradeCallbackResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_grader_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   8,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_grader_proto_goTypes,
		DependencyIndexes: file_grader_proto_depIdxs,
		MessageInfos:      file_grader_proto_msgTypes,
	}.Build()
	File_grader_proto = out.File
	file_grader_proto_rawDesc = nil
	file_grader_proto_goTypes = nil
	file_grader_proto_depIdxs = nil
}
