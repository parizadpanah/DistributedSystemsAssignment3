.PHONY: proto build run clean stop

proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		replication.proto

build: proto
	go mod tidy
	go build -o kvstore main.go

run:
	docker-compose up --build

stop:
	docker-compose down

clean: stop
	rm -rf data-primary data-backup1 data-backup2
	rm -f kvstore
	docker-compose down -v

test:
	go test -v ./...