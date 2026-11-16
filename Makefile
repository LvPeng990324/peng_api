.PHONY: all build

all:
	cd web && npm run build
	go build -o peng_api
	go build -o peng_api.exe

build: all
