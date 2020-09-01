// Copyright 2018 Twitch Interactive, Inc.  All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the License is
// located at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// or in the "license" file accompanying this file. This file is distributed on
// an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package main

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/protoc-gen-go/descriptor"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	proto_to_insomnia "github.com/thesilentg/proto-to-insomnia"
	"github.com/twitchtv/protogen"
	"github.com/twitchtv/protogen/stringutils"
	"github.com/twitchtv/protogen/typemap"
)

const (
	protoFileExtension = ".proto"
	maxDepth           = 25
)

func main() {
	t := insomniaenv{}
	protogen.RunProtocPlugin(&t)
}

type insomniaenv struct {
	registry *typemap.Registry
}

// InsomniaExport describes the structure of an Insomnia export
type InsomniaExport struct {
	ExportType   string        `json:"_type"`
	ExportFormat int           `json:"__export_format"`
	ExportSource string        `json:"__export_source"`
	Resources    []interface{} `json:"resources"`
}

// Resource describes the structure of an Insomnia resource
type Resource struct {
	Type     string  `json:"_type"`
	ID       string  `json:"_id"`
	ParentID *string `json:"parentId"`
	Name     string  `json:"name"`
}

// Workspace describes the structure of an Insomnia Workspace
type Workspace struct {
	Resource
}

// Environment describes the structure of an Insomnia Environment
type Environment struct {
	Resource
	Data map[string]string `json:"data"`
}

// RequestGroup describes the structure of an Insomnia RequestGroup
type RequestGroup struct {
	Resource
	Environment map[string]string `json:"environment"`
}

// Request describes the structure of an Insomnia Request
type Request struct {
	Resource
	Method      string              `json:"method"`
	URL         string              `json:"url"`
	Headers     []map[string]string `json:"headers"`
	Body        RequestBody         `json:"body"`
	Description string              `json:"description"`
}

// RequestBody describes the structure of an Insomnia RequestBody
type RequestBody struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

func (e *insomniaenv) Generate(in *plugin.CodeGeneratorRequest) (*plugin.CodeGeneratorResponse, error) {
	filesToGenerate, err := protogen.FilesToGenerate(in)
	if err != nil {
		return nil, err
	}

	e.registry = typemap.New(in.ProtoFile)

	resp := new(plugin.CodeGeneratorResponse)
	for _, file := range filesToGenerate {
		respFile, err := e.generate(file, in.Parameter)
		if err != nil {
			return nil, err
		}

		resp.File = append(resp.File, respFile)
	}
	return resp, nil
}

func (e *insomniaenv) generate(file *descriptor.FileDescriptorProto, param *string) (*plugin.CodeGeneratorResponse_File, error) {
	resp := new(plugin.CodeGeneratorResponse_File)
	if len(file.Service) == 0 {
		return nil, nil
	}

	insomniaExport := InsomniaExport{
		ExportType:   "export",
		ExportFormat: 3,
		ExportSource: "protoc-gen-insomniaenv",
	}

	resources := []interface{}{}
	workspace, workspaceID := generateWorkspace(file)
	resources = append(resources, workspace)

	envs, err := generateEnvironment(workspaceID, param)
	if err != nil {
		return nil, err
	}

	for _, env := range envs {
		resources = append(resources, env)
	}

	resources = append(resources, e.generateMethods(workspaceID, file)...)
	insomniaExport.Resources = resources

	b, err := json.MarshalIndent(insomniaExport, "", "\t")
	if err != nil {
		return nil, err
	}

	fileWithoutPath := strings.TrimSuffix(file.GetName(), filepath.Ext(file.GetName()))
	resp.Name = proto.String(fmt.Sprintf("%s-insomnia-env.json", fileWithoutPath))
	resp.Content = proto.String(string(b))

	return resp, nil
}

func (e *insomniaenv) generateMethods(workspaceID string, file *descriptor.FileDescriptorProto) []interface{} {
	resources := make([]interface{}, 0)
	for _, service := range file.Service {
		requestGroupID := fmt.Sprintf("request_group-%s", *service.Name)
		resources = append(resources, RequestGroup{
			Resource: Resource{
				Type:     "request_group",
				ID:       requestGroupID,
				ParentID: &workspaceID,
				Name:     *service.Name,
			},
			Environment: map[string]string{
				*service.Name: fmt.Sprintf("{{ base_url }}%s", pathPrefix(file, service)),
			},
		})

		md5HashFunc := md5.New()
		requests := make([]Request, 0)
		for _, method := range service.Method {
			// We don't want the addition of a new method to change the randomly
			// generated values for all of the other methods. Set a deterministic
			// seed based on method Name
			sum := md5HashFunc.Sum([]byte(method.GetName()))[:8]
			rand.Seed(int64(binary.BigEndian.Uint64(sum)))
			msg := e.registry.MessageDefinition(method.GetInputType())
			output := e.generateMockMessage(msg, 0)
			comment, _ := e.registry.MethodComments(file, service, method)

			requests = append(requests, Request{
				Resource: Resource{
					Type:     "request",
					ID:       fmt.Sprintf("request-%s-%s", service.GetName(), method.GetName()),
					ParentID: &requestGroupID,
					Name:     *method.Name,
				},
				Method: "POST",
				Headers: []map[string]string{
					{
						"name":  "Content-Type",
						"value": "application/json",
					},
				},
				URL: fmt.Sprintf("{{%s}}%s", service.GetName(), method.GetName()),
				Body: RequestBody{
					MimeType: "application/json",
					Text:     output,
				},
				Description: comment.Leading,
			})
		}

		// Put the methods in alphabetical orders
		sort.SliceStable(requests, func(i, j int) bool {
			return requests[i].ID < requests[j].ID
		})
		for _, request := range requests {
			resources = append(resources, request)
		}

	}
	return resources
}

func generateEnvironment(workspaceID string, param *string) ([]Environment, error) {
	envs := make([]Environment, 0)
	baseEnvName := "BaseEnvironment"
	envs = append(envs, Environment{
		Resource: Resource{
			Type:     "environment",
			ID:       baseEnvName,
			ParentID: &workspaceID,
			Name:     "Base",
		},
		Data: map[string]string{},
	})

	if param != nil && len(*param) > 0 {
		var config proto_to_insomnia.Config
		err := json.Unmarshal([]byte(*param), &config)
		if err != nil {
			return []Environment{}, err
		}

		for name, url := range config.Environments {
			envs = append(envs, Environment{
				Resource: Resource{
					Type:     "environment",
					ID:       name,
					ParentID: &baseEnvName,
					Name:     name,
				},
				Data: map[string]string{
					"base_url": url,
				},
			})
		}
	}

	envs = append(envs, Environment{
		Resource: Resource{
			Type:     "environment",
			ID:       "LocalhostHttps",
			ParentID: &baseEnvName,
			Name:     "Localhost - Https",
		},
		Data: map[string]string{
			"base_url": "https://localhost:8000",
		},
	})

	envs = append(envs, Environment{
		Resource: Resource{
			Type:     "environment",
			ID:       "LocalhostHttp",
			ParentID: &baseEnvName,
			Name:     "Localhost - Http",
		},
		Data: map[string]string{
			"base_url": "http://localhost:8000",
		},
	})

	return envs, nil
}

func (e *insomniaenv) generateMockMessage(messageDefinition *typemap.MessageDefinition, depth int) string {
	var output string
	numFields := len(messageDefinition.Descriptor.Field)

	// This is quite a mess
	output += "{\n"
	for idx, field := range messageDefinition.Descriptor.Field {
		// Handle repeated case
		if field.GetLabel() == descriptor.FieldDescriptorProto_LABEL_REPEATED {
			output += strings.Repeat("\t", depth+1) + "\"" + field.GetJsonName() + "\": [\n"
			for i := 0; i < 3; i++ {
				output += strings.Repeat("\t", depth+2)
				output += e.generateMockField(messageDefinition, field, depth+1)
				if i < 2 {
					output += ",\n"
				} else {
					output += "\n"
				}
			}
			if idx != numFields-1 {
				output += strings.Repeat("\t", depth+1) + "],\n"
			} else {
				output += strings.Repeat("\t", depth+1) + "]\n"
			}
		} else {
			// Handle singular case
			output += strings.Repeat("\t", depth+1) + "\"" + field.GetJsonName() + "\": " + e.generateMockField(messageDefinition, field, depth)
			if idx != numFields-1 {
				output += ",\n"
			} else {
				output += "\n"
			}
		}
	}
	output += strings.Repeat("\t", depth) + "}"
	return output
}

func (e *insomniaenv) generateMockField(messageDefinition *typemap.MessageDefinition, field *descriptor.FieldDescriptorProto, depth int) string {
	// In case of any strange behavior which causes us to continue processing, I've added this as a fallback to ensure that we don't hang forever
	if depth >= maxDepth {
		return fmt.Sprintf("Max request depth of %d reached. This may indicate some error with proto-to-insomnia parsing logic", maxDepth)
	}

	// Special case these since they are interpreted differently
	if field.GetTypeName() == ".google.protobuf.Timestamp" {
		return fmt.Sprintf("\"%s\"", randomTimestamp())
	} else if field.GetTypeName() == ".google.protobuf.Duration" {
		return fmt.Sprintf("\"%d.%03ds\"", rand.Intn(1000), rand.Intn(100))
	} else if field.GetTypeName() == ".google.protobuf.Struct" {
		return fmt.Sprintf("{\"this field named %s contains\": \"a dynamically typed map.\", \"As input,\": \"you can pass any JSON object\"}", *field.Name)
	}

	switch fieldType := *field.Type; fieldType {
	case descriptor.FieldDescriptorProto_TYPE_DOUBLE:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_FLOAT:
		randFloat := 1000*rand.Float32() - 500
		return fmt.Sprintf("%.4f", randFloat)
	case descriptor.FieldDescriptorProto_TYPE_SFIXED32:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_SFIXED64:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_SINT32:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_SINT64:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_INT64:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_INT32:
		randInt := rand.Intn(1000) - 500
		return strconv.Itoa(randInt)
	case descriptor.FieldDescriptorProto_TYPE_FIXED64:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_FIXED32:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_UINT32:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_UINT64:
		randUInt := rand.Intn(1000)
		return strconv.Itoa(randUInt)
	case descriptor.FieldDescriptorProto_TYPE_BOOL:
		if rand.Float32() < 0.5 {
			return "false"
		}
		return "true"
	case descriptor.FieldDescriptorProto_TYPE_STRING:
		return fmt.Sprintf("\"%s\"", generateRandomString(10))
	case descriptor.FieldDescriptorProto_TYPE_MESSAGE:
		fallthrough
	case descriptor.FieldDescriptorProto_TYPE_BYTES:
		msg := e.registry.MessageDefinition(field.GetTypeName())
		if msg == nil {
			return fmt.Sprintf("\"Message %s could not be found\"", field.GetTypeName())
		}
		return e.generateMockMessage(msg, depth+1)
	case descriptor.FieldDescriptorProto_TYPE_ENUM:
		return generateMockEnumValue(messageDefinition, field)
	}
	return "\"PARSE_ERROR\""
}

func generateMockEnumValue(messageDefinition *typemap.MessageDefinition, field *descriptor.FieldDescriptorProto) string {
	// Check enums defined in the message
	for _, enumType := range messageDefinition.Descriptor.EnumType {
		if checkEnumMessageMatch(enumType, messageDefinition, field) {
			return fmt.Sprintf("\"%s\"", generateRandomEnumValue(enumType))
		}
	}
	// Check enums defined in the file
	for _, enumType := range messageDefinition.File.EnumType {
		if checkEnumFileMatch(enumType, messageDefinition.File, field) {
			return fmt.Sprintf("\"%s\"", generateRandomEnumValue(enumType))
		}
	}
	return fmt.Sprintf("\"%s\"", field.GetTypeName())
}

func randomTimestamp() string {
	randomTime := rand.Int63n(1000000000) + 94608000
	randomNow := time.Unix(randomTime, 0)
	return randomNow.Format(time.RFC3339)
}

func generateRandomEnumValue(enum *descriptor.EnumDescriptorProto) string {
	return enum.GetValue()[rand.Intn(len(enum.GetValue()))].GetName()
}

func checkEnumMessageMatch(enum *descriptor.EnumDescriptorProto, messageDefinition *typemap.MessageDefinition, field *descriptor.FieldDescriptorProto) bool {
	return field.GetTypeName() == fmt.Sprintf(".%s.%s.%s", messageDefinition.File.GetPackage(), messageDefinition.Descriptor.GetName(), enum.GetName())
}

func checkEnumFileMatch(enum *descriptor.EnumDescriptorProto, file *descriptor.FileDescriptorProto, field *descriptor.FieldDescriptorProto) bool {
	return field.GetTypeName() == fmt.Sprintf(".%s.%s", file.GetPackage(), enum.GetName())
}

func generateRandomString(n int) string {
	var letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func generateWorkspace(file *descriptor.FileDescriptorProto) (Workspace, string) {
	id := fmt.Sprintf("workspace-%s-%s", file.GetName(), file.GetPackage())
	return Workspace{
		Resource: Resource{
			Type:     "workspace",
			ID:       id,
			ParentID: nil,
			Name:     getFileName(*file.Name),
		},
	}, id
}

func getFileName(s string) string {
	return strings.Title(trimSuffix(s, protoFileExtension))
}

func trimSuffix(s, suffix string) string {
	if strings.HasSuffix(s, suffix) {
		s = s[:len(s)-len(suffix)]
	}
	return s
}

func pkgName(file *descriptor.FileDescriptorProto) string {
	return file.GetPackage()
}

func fullServiceName(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) string {
	name := stringutils.CamelCase(service.GetName())
	if pkg := pkgName(file); pkg != "" {
		name = pkg + "." + name
	}
	return name
}

// pathPrefix returns the base path for all methods handled by a particular
// service. It includes a trailing slash. (for example
// "/twirp/twitch.example.Haberdasher/").
func pathPrefix(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto) string {
	return fmt.Sprintf("/twirp/%s/", fullServiceName(file, service))
}

// pathFor returns the complete path for requests to a particular method on a
// particular service.
func pathFor(file *descriptor.FileDescriptorProto, service *descriptor.ServiceDescriptorProto, method *descriptor.MethodDescriptorProto) string {
	return pathPrefix(file, service) + stringutils.CamelCase(method.GetName())
}
