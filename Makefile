SOURCE_FILES := $(shell find . \( -name '*.go' -not -path './vendor*' \))

all:	amigo_bot

amigo_bot:	vendor $(SOURCE_FILES)
	go build -o amigo_bot .

vendor:
	glide install

amigo_bot_linux:	vendor $(SOURCE_FILES)
	GOOS=linux GOARCH=amd64 go build -o amigo_bot_linux .
	scp amigo_bot_linux ctf-admin.quaxio.com:~/
