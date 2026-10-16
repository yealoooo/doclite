# 客户端的编译和运行（界面取 WEB_VERSION 指定版本的 web 产物包嵌进去）。

WAILS ?= $(HOME)/go/bin/wails3

.PHONY: build run

build:
	GOTOOLCHAIN=local $(WAILS) task build

run: build
	./bin/Doclite
