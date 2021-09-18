go build -o cmd/hmps server/hmps.go
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o cmd/hmpc.exe client/hmpc.go