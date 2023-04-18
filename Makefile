BMM_PATH ?= .

.PHONY: splitter
splitter:
ifeq ($(OS),Windows_NT)
	@echo "This target is only for non-Windows platforms"
else
	CC=x86_64-w64-mingw32-gcc CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -o $(BMM_PATH)/scripts/splitter/splitter.exe ./scripts/splitter/main.go
endif
