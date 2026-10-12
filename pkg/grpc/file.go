package grpc

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	autograder_pb "autograder-server/pkg/api/proto"
	model_pb "autograder-server/pkg/model/proto"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (a *AutograderService) InitDownload(
	ctx context.Context, request *autograder_pb.InitDownloadRequest,
) (*autograder_pb.InitDownloadResponse, error) {
	submission := ctx.Value(submissionCtxKey{}).(*model_pb.Submission)
	if request.GetIsDirectory() {
		fn := fmt.Sprintf("submission_%d.zip", request.GetSubmissionId())
		payloadPB := &autograder_pb.DownloadTokenPayload{
			RealPath: submission.GetPath(), Filename: fn, IsDirectory: true, SubmissionId: request.GetSubmissionId(),
		}
		ss, err := a.signPayloadToken(a.downloadJWTSignKey, payloadPB, time.Now().Add(TransferTokenValidDuration))
		if err != nil {
			return nil, status.Error(codes.Internal, CodeSignJwt)
		}
		resp := &autograder_pb.InitDownloadResponse{Token: ss, Filename: fn}
		return resp, nil
	}
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {
		return nil, status.Error(codes.InvalidArgument, CodeInvalidFilename)
	}
	realpath := path.Join(submission.GetPath(), filename)
	if request.GetIsOutput() {
		realpath = path.Join(fmt.Sprintf("runs/submissions/%d/results/outputs", request.GetSubmissionId()), filename)
	}
	_, fn := path.Split(filename)
	file, err := a.ls.Open(ctx, realpath)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeOpenFile)
	}
	defer file.Close()
	size, err := a.ls.Size(ctx, realpath)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeFileSize)
	}
	head := make([]byte, MIMESniffLength)
	n, _ := io.ReadFull(file, head)
	fileType := http.DetectContentType(head[:n])
	fileTypePB := autograder_pb.DownloadFileType_Binary
	if fileType == "application/pdf" {
		fileTypePB = autograder_pb.DownloadFileType_PDF
	} else if strings.HasPrefix(fileType, "image/") {
		fileTypePB = autograder_pb.DownloadFileType_Image
	} else if strings.HasPrefix(fileType, "text/plain") {
		fileTypePB = autograder_pb.DownloadFileType_Text
	}
	payloadPB := &autograder_pb.DownloadTokenPayload{RealPath: realpath, Filename: fn}
	ss, err := a.signPayloadToken(a.downloadJWTSignKey, payloadPB, time.Now().Add(TransferTokenValidDuration))
	if err != nil {
		return nil, status.Error(codes.Internal, CodeSignJwt)
	}
	resp := &autograder_pb.InitDownloadResponse{FileType: fileTypePB, Token: ss, Filename: fn, Filesize: size}
	return resp, nil
}

func isFilenameInvalid(filename string) bool {
	return len(filename) == 0 || strings.Contains(filename, "..") || filepath.IsAbs(filename)
}

func (a *AutograderService) getManifestPath(manifestId uint64) string {
	return fmt.Sprintf("uploads/manifests/%d", manifestId)
}

func (a *AutograderService) DeleteFileInManifest(
	ctx context.Context, request *autograder_pb.DeleteFileInManifestRequest,
) (*autograder_pb.DeleteFileInManifestResponse, error) {
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {
		return nil, status.Error(codes.InvalidArgument, CodeInvalidFilename)
	}
	manifestId := request.GetManifestId()
	mu, err := a.manifestRepo.LockManifest(ctx, manifestId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, CodeInvalidManifestId)
	}
	_, _ = a.manifestRepo.DeleteFileInManifest(ctx, filename, manifestId)
	mu.Unlock()
	err = a.ls.Delete(ctx, filepath.Join(a.getManifestPath(manifestId), filename))
	if err != nil && err != os.ErrNotExist {
		return nil, status.Error(codes.InvalidArgument, CodeDeleteFailed)
	}
	return &autograder_pb.DeleteFileInManifestResponse{}, nil
}

func (a *AutograderService) CreateManifest(
	ctx context.Context, request *autograder_pb.CreateManifestRequest,
) (*autograder_pb.CreateManifestResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	assignment := ctx.Value(assignmentCtxKey{}).(*model_pb.Assignment)
	id, err := a.manifestRepo.CreateManifest(
		ctx, user.GetUserId(), request.GetAssignmentId(), assignment.GetUploadLimit(),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, CodeCreateManifest)
	}
	resp := &autograder_pb.CreateManifestResponse{ManifestId: id}
	return resp, nil
}

// validateSubmitters returns the de-duplicated submitter set for a new
// submission. Every submitter must be a member of the course and, unless the
// caller is staff, the caller must be among them: otherwise a submission could
// be recorded under arbitrary users and grant the caller read access to it via
// RequireSubmissionRead. An empty list means "just the caller".

func (a *AutograderService) InitUpload(
	ctx context.Context, request *autograder_pb.InitUploadRequest,
) (*autograder_pb.InitUploadResponse, error) {
	user := ctx.Value(userInfoCtxKey{}).(*autograder_pb.UserTokenPayload)
	manifest, err := a.manifestRepo.GetManifest(ctx, request.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, CodeInvalidManifestId)
	}
	if manifest.GetUserId() != user.GetUserId() {
		return nil, status.Error(codes.InvalidArgument, CodeDifferentUser)
	}
	mu, err := a.manifestRepo.LockManifest(ctx, request.GetManifestId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, CodeLockManifest)
	}
	total, err := a.manifestRepo.GetManifestFilesize(ctx, request.GetManifestId())
	originalFile, _ := a.manifestRepo.GetManifestFileMetadata(
		ctx, request.GetFilename(), request.GetManifestId(),
	)
	mu.Unlock()
	if err != nil {
		return nil, status.Error(codes.Internal, CodeGetSize)
	}
	if originalFile != nil {
		total -= originalFile.GetFilesize()
	}
	if request.GetFilesize() == 0 || total+request.GetFilesize() > manifest.GetUploadLimit() {
		return nil, status.Error(codes.ResourceExhausted, CodeNoSpace)
	}
	filename := request.GetFilename()
	if isFilenameInvalid(filename) {
		return nil, status.Error(codes.InvalidArgument, CodeInvalidFilename)
	}
	key := a.uploadJWTSignKey

	payload := &autograder_pb.UploadTokenPayload{
		ManifestId:  request.ManifestId,
		Filename:    path.Clean(filename),
		Filesize:    request.GetFilesize(),
		UploadLimit: manifest.GetUploadLimit(),
	}
	ss, err := a.signPayloadToken(key, payload, time.Now().Add(TransferTokenValidDuration))
	if err != nil {
		return nil, err
	}
	resp := &autograder_pb.InitUploadResponse{Token: ss}
	return resp, nil
}

func (a *AutograderService) HandleFileDownload(w http.ResponseWriter, r *http.Request) {
	downloadTokenString := strings.TrimSpace(r.URL.Query().Get("token"))
	fn := chi.URLParam(r, "filename")
	payload, err := a.parseTokenPayload(a.downloadJWTSignKey, downloadTokenString)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var payloadPB autograder_pb.DownloadTokenPayload
	err = proto.Unmarshal(payload, &payloadPB)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	logger := zap.L().With(zap.String("filename", payloadPB.Filename), zap.String("realPath", payloadPB.RealPath))
	if !payloadPB.GetIsDirectory() {
		if err != nil || fn != payloadPB.GetFilename() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, err := a.ls.Open(r.Context(), payloadPB.GetRealPath())
		if err != nil {
			logger.Error("FileDownload.FS.Open", zap.Error(err))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer data.Close()
		w.Header().Add("Content-disposition", "attachment; filename="+fn)
		w.WriteHeader(http.StatusOK)
		_, err = io.Copy(w, data)
		if err != nil {
			logger.Error("FileDownload.FS.Copy", zap.Error(err))
			return
		}
	} else {
		submission, err := a.submissionRepo.GetSubmission(r.Context(), payloadPB.GetSubmissionId())
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Open every file before writing any response body: once the zip stream
		// starts the client has already received 200 and the zip header, so a
		// mid-stream Open failure can only hand back a truncated archive. Opening
		// up front lets us fail with a clean error status instead.
		readers := make([]io.ReadCloser, 0, len(submission.Files))
		defer func() {
			for _, rc := range readers {
				rc.Close()
			}
		}()
		for _, f := range submission.Files {
			data, err := a.ls.Open(r.Context(), filepath.Join(submission.Path, f))
			if err != nil {
				logger.Error("FileDownload.GenerateZip.OpenLocal", zap.Error(err))
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			readers = append(readers, data)
		}
		w.Header().Add("Content-disposition", "attachment; filename="+fn)
		zw := zip.NewWriter(w)
		defer zw.Close()
		for i, f := range submission.Files {
			zf, err := zw.Create(f)
			if err != nil {
				logger.Error("FileDownload.GenerateZip.CreateZipFile", zap.Error(err))
				return
			}
			_, err = io.Copy(zf, readers[i])
			if err != nil {
				logger.Error("FileDownload.GenerateZip.IOCopy", zap.Error(err))
				return
			}
		}
	}
}

func (a *AutograderService) HandleFileUpload(w http.ResponseWriter, r *http.Request) {
	var err error
	normalizedContentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-type")))
	uploadTokenString := strings.TrimSpace(r.Header.Get("Upload-token"))
	payload, err := a.parseTokenPayload(a.uploadJWTSignKey, uploadTokenString)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var payloadPB autograder_pb.UploadTokenPayload
	err = proto.Unmarshal(payload, &payloadPB)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(normalizedContentType, "multipart/form-data; boundary") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = r.ParseMultipartForm(MaxMultipartFormParseMemory)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	uploadFile, header, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if header.Size <= 0 {
		w.WriteHeader(http.StatusLengthRequired)
		return
	}
	fileHeader := make([]byte, MIMESniffLength)
	if _, err = io.ReadFull(uploadFile, fileHeader); err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = uploadFile.Seek(0, 0)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// TODO: filter MIME
	mu, err := a.manifestRepo.LockManifest(r.Context(), payloadPB.GetManifestId())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	originalFile, _ := a.manifestRepo.GetManifestFileMetadata(
		r.Context(), payloadPB.GetFilename(), payloadPB.GetManifestId(),
	)
	if originalFile != nil {
		_, err = a.manifestRepo.DeleteFileInManifest(r.Context(), payloadPB.GetFilename(), payloadPB.GetManifestId())
		if err != nil {
			zap.L().Error("FileUpload.CleanupOld", zap.Error(err))
		}
	}
	current, err := a.manifestRepo.GetManifestFilesize(r.Context(), payloadPB.GetManifestId())
	if err != nil {
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if current+uint64(header.Size) > payloadPB.GetUploadLimit() {
		mu.Unlock()
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}
	mu.Unlock()
	destPath := filepath.Join(a.getManifestPath(payloadPB.GetManifestId()), payloadPB.GetFilename())
	err = a.ls.Put(
		r.Context(),
		destPath,
		uploadFile,
	)
	if err != nil {
		zap.L().Error("FileUpload.PutFile", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	mu, err = a.manifestRepo.LockManifest(r.Context(), payloadPB.GetManifestId())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, err = a.manifestRepo.AddFileToManifest(
		r.Context(), payloadPB.GetFilename(), payloadPB.GetManifestId(),
		uint64(header.Size),
	)
	mu.Unlock()
	if err != nil {
		zap.L().Error("FileUpload.AddFileToManifest", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *AutograderService) manifestGarbageCollect() {
	ctx := a.gcCtx
	ch := make(chan uint64)
	zap.L().Debug("Manifest.GC.Start")
	go a.manifestRepo.GarbageCollect(ctx, ch)
	for manifestId := range ch {
		manifestPath := a.getManifestPath(manifestId)
		err := a.ls.Delete(ctx, manifestPath)
		if err != nil {
			zap.L().Error("Manifest.GC.DeleteFile", zap.String("manifestPath", manifestPath), zap.Error(err))
		}
	}
}

func (a *AutograderService) PushFile(w http.ResponseWriter, r *http.Request) {
	uploadPath := r.URL.Path

	err := a.ls.Put(
		r.Context(),
		uploadPath,
		r.Body,
	)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
