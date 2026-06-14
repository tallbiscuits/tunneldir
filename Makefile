PREFIX ?= /usr/local
BIN    := tunneldir

.PHONY: build install uninstall dist clean test

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/tunneldir

install: build
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 $(BIN) $(DESTDIR)$(PREFIX)/bin/$(BIN)
	@echo "installed $(DESTDIR)$(PREFIX)/bin/$(BIN)"

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BIN)

dist:
	./build.sh all

test:
	go test ./...

clean:
	rm -f $(BIN)
	rm -rf dist
