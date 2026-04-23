CC ?= cc
PREFIX ?= /usr/local

hello-src: hello.c
	$(CC) -o hello-src hello.c

install: hello-src
	install -d $(PREFIX)/bin
	install hello-src $(PREFIX)/bin/hello-src

clean:
	rm -f hello-src

.PHONY: install clean
