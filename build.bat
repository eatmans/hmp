go build -o cmd/hmpc.exe client/hmpc.go

SET CGO_ENABLED=0
SET GOOS=linux
SET GOARCH=amd64
go build -o cmd/hmps server/hmps.go