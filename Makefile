.PHONY: install build-ui restart migrate doctor status stop test
install build-ui restart migrate doctor status stop:
	./relay $@
test:
	go test ./...
