.PHONY: all build

all:
	cd web && npm run build
	go build -o peng_api

build: all
