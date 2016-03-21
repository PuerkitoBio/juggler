cmdnames = server client callee load
cmds = $(addprefix juggler-, $(cmdnames))

# run `make` to build all commands.
# run `make flags=-race` to build with race detector.
# assign any valid build flag to flags to build with that set of flags.
all: $(cmds)

$(cmds):
	go build $(flags) ./cmd/$@ 

cluster:
	go run ./internal/start-cluster/main.go

.PHONY: all $(cmds) cluster

