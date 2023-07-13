# Autograder

Autograder is a web-based system designed for software project auto-grading, utilizing Docker.

This repository contains the server-side code. For the web-based client-side code, please visit 
[https://github.com/howardlau1999/autograder-web](https://github.com/howardlau1999/autograder-web).

For documentation, please head for [https://autograder-docs.howardlau.me](https://autograder-docs.howardlau.me)

知乎文章介绍：[Autograder - 一个适合项目作业的评测系统](https://zhuanlan.zhihu.com/p/479027855)

## Build

Go 1.17+ is needed for building the server.

To build without client-side webpage code (which means you need a reverse-proxy like nginx to serve the static contents)

```bash
mkdir -p pkg/web/dist
go build -tags containers_image_openpgp -o autograder-server cmd/autograder_server.go
```

To build the grader service

```bash
go build -tags containers_image_openpgp -o autograder-grader cmd/autograder_grader.go
```

To build with the client-side webpage code, Node.js 16+ is needed.

```bash
git submodule update --init 
npm install -g @angular/cli
cd web
npm install && npm install --no-save --ignore-scripts vcd-stream && ng build --output-path ../pkg/web/dist
cd ..
go build -tags containers_image_openpgp -o autograder-server cmd/autograder_server.go
```
