BIN := pntgw
PKG := ./cmd/pntgw
LDFLAGS := -s -w

.PHONY: all build erx clean

all: build

build:
	go build -ldflags="$(LDFLAGS)" -o $(BIN) $(PKG)

erx:
	CGO_ENABLED=0 GOOS=linux GOARCH=mipsle go build -ldflags="$(LDFLAGS)" -o $(BIN)-mipsle $(PKG)
	@ls -lh $(BIN)-mipsle
	@file $(BIN)-mipsle

deploy: erx
	scp $(BIN)-mipsle ubnt@192.168.10.1:/tmp/pntgw.new
	ssh ubnt@192.168.10.1 'sudo mv /tmp/pntgw.new /config/pntgw && sudo chmod +x /config/pntgw && sudo systemctl restart pntgw 2>/dev/null || true'

clean:
	rm -f $(BIN) $(BIN)-mipsle
