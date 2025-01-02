set_env=GOMODPROXY="https://goproxy.cn,https://proxy.golang.org,direct" GOPRIVATE="*.everphoto.cn"
local_package=github.com/Luoyuhao/go-cacheloader

.PHONY: lint
.PHONY: test

run:
	make lint
	make test

lint:
	@$(set_env) go fmt ./...
	@$(set_env) go vet ./...
	@$(set_env) goimports -local $(local_package) -w .
	@$(set_env) go mod tidy

test:
	$(set_env) test -z "$$(gofmt -l .)"
	$(set_env) test -z "$$(goimports -local $(local_package) -d .)"
	GO111MODULE=on echo 'mode: atomic' > c.out && \
  go list ./... | xargs -n1 -I{} sh -c 'LOCAL_TEST=true go test -v -covermode=atomic -coverprofile=coverage.tmp -coverpkg=./... -parallel 1 -p 1 -count=1 -gcflags=-l {} && tail -n +2 coverage.tmp >> c.out' && \
  ( rm coverage.tmp || echo "coverage.tmp not exist") && \
  ( rm c.out || echo "c.out not exist")

html:
	go tool cover -html=c.out
