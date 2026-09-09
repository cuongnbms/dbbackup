VERSION=1.7

.PHONY: test build push

test:
	go vet ./... && go test ./...

build:
	docker build -t cuongnb14/db-backup:${VERSION} .

push:
	docker push cuongnb14/db-backup:${VERSION}
