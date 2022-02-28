$CWD=$PWD.Path
protoc --go_out=.. --go-grpc_out=.. -Iproto proto/api.proto proto/model.proto proto/grader.proto
protoc --plugin="protoc-gen-ts=$CWD/web/node_modules/.bin/protoc-gen-ts.cmd" --js_out="import_style=commonjs,binary:web/src/app/api/proto" -Iproto --ts_out="service=grpc-web:web/src/app/api/proto" proto/api.proto proto/model.proto