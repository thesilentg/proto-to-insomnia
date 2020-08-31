build:
	cd ./protoc-gen-insomniaenv && go install

build-test: build
	protoc --proto_path=$(CURDIR)/test-data --insomniaenv_out=$(CURDIR)/test-data/ --insomniaenv_opt="{\"environments\": {\"Staging\":\"http://www.staging-host.com\",\"Production\":\"http://www.production-host.com\"}}" $(CURDIR)/test-data/test.proto
