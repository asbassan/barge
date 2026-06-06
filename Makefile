BINARY   = barge.exe
CMD      = ./cmd/barge
VERSION  = 0.1.0
LDFLAGS  = -X main.version=$(VERSION)

.PHONY: build clean test vet fmt

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	del /f $(BINARY) 2>nul || true
