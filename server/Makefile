# server 的编译和运行。run 在当前目录（server/）运行，读这里的 .env；Ctrl+C 退出。

.PHONY: build run

build:
	GOTOOLCHAIN=local go build -o bin/doclite-server ./cmd/server

run: build
	./bin/doclite-server
