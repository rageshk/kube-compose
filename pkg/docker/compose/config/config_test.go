package config

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/kube-compose/kube-compose/internal/pkg/fs"
	"github.com/kube-compose/kube-compose/internal/pkg/util"
	"github.com/pkg/errors"
)

const testDockerComposeYml = "/docker-compose.yaml"
const testDockerComposeYmlIOError = "/docker-compose.io-error.yaml"
const testDockerComposeYmlInvalidVersion = "/docker-compose.invalid-version.yml"
const testDockerComposeYmlInterpolationIssue = "/docker-compose.interpolation-issue.yml"
const testDockerComposeYmlDecodeIssue = "/docker-compose.decode-issue.yml"
const testDockerComposeYmlExtends = "/docker-compose.extends.yml"
const testDockerComposeYmlExtendsCycle = "/docker-compose.extends-cycle.yml"
const testDockerComposeYmlExtendsIOError = "/docker-compose.extends-io-error.yml"
const testDockerComposeYmlExtendsDoesNotExist = "/docker-compose.extends-does-not-exist.yml"
const testDockerComposeYmlExtendsDoesNotExistFile = "/docker-compose.extends-does-not-exist-file.yml"
const testDockerComposeYmlExtendsInvalidDependsOn = "/docker-compose.extends-invalid-depends-on.yml"
const testDockerComposeYmlDependsOnDoesNotExist = "/docker-compose.depends-on-does-not-exist.yml"
const testDockerComposeYmlDependsOnCycle = "/docker-compose.depends-on-cycle.yml"
const testDockerComposeYmlDependsOn = "/docker-compose.depends-on.yml"

var mockFS = fs.NewInMemoryUnixFileSystem(map[string]fs.InMemoryFile{
	testDockerComposeYml: {
		Content: []byte(`testservice:
  entrypoint: []
  command: ["bash", "-c", "echo 'Hello World!'"]
  image: ubuntu:latest
  volumes:
  - "aa:bb:cc"
`),
	},
	testDockerComposeYmlIOError: {
		Error: errors.New("unknown error 1"),
	},
	testDockerComposeYmlInvalidVersion: {
		Content: []byte("version: ''"),
	},
	testDockerComposeYmlInterpolationIssue: {
		Content: []byte(`version: '2.3'
services:
  testservice:
    image: '$'
`),
	},
	testDockerComposeYmlDecodeIssue: {
		Content: []byte(`version: '2.3'
services:
  testservice:
    environment: 3
`),
	},
	testDockerComposeYmlExtends: {
		Content: []byte(`version: '2.3'
services:
  service1:
    environment:
      KEY1: VALUE1
    extends:
      service: service2
  service2:
    environment:
      KEY2: VALUE2
    extends:
      file: '` + testDockerComposeYml[1:] + `'
      service: testservice
  service3:
    extends:
      file: '` + testDockerComposeYml[1:] + `'
      service: testservice
`),
	},
	testDockerComposeYmlExtendsCycle: {
		Content: []byte(`version: '2.3'
services:
  service1:
    extends:
      service: service2
  service2:
    extends:
      service: service3
  service3:
    extends:
      service: service2
`),
	},
	testDockerComposeYmlExtendsIOError: {
		Content: []byte(`version: '2.3'
services:
  service1:
    environment:
      KEY2: VALUE2
    extends:
      file: '` + testDockerComposeYmlIOError + `'
      service: testservice
`),
	},
	testDockerComposeYmlExtendsDoesNotExist: {
		Content: []byte(`version: '2.3'
services:
  service1:
    extends:
      service: service2
`),
	},
	testDockerComposeYmlExtendsDoesNotExistFile: {
		Content: []byte(`version: '2.3'
services:
  service1:
    extends:
      file: '` + testDockerComposeYml + `'
      service: service2
`),
	},
	testDockerComposeYmlExtendsInvalidDependsOn: {
		Content: []byte(`version: '2.3'
services:
  service1:
    extends:
      service: service2
  service2:
    depends_on:
    - service1
`),
	},
	testDockerComposeYmlDependsOnDoesNotExist: {
		Content: []byte(`version: '2.3'
services:
  service1:
    depends_on:
    - service2
`),
	},
	testDockerComposeYmlDependsOnCycle: {
		Content: []byte(`version: '2.3'
services:
  service1:
    depends_on:
    - service2
  service2:
    depends_on:
    - service1
`),
	},
	testDockerComposeYmlDependsOn: {
		Content: []byte(`version: '2.3'
services:
  service1:
    depends_on:
    - service2
  service2:
    depends_on:
      service3:
        condition: service_healthy
  service3: {}
`),
	},
})

var mockFileSystemStandardFileError = fs.NewInMemoryUnixFileSystem(map[string]fs.InMemoryFile{
	"/docker-compose.yml": {
		Error: errors.New("unknown error 2"),
	},
})

func withMockFS(cb func()) {
	original := fs.OS
	defer func() {
		fs.OS = original
	}()
	fs.OS = mockFS
	cb()
}

func newTestConfigLoader(env map[string]string) *configLoader {
	c := &configLoader{
		environmentGetter:     mapValueGetter(env),
		loadResolvedFileCache: map[string]*loadResolvedFileCacheItem{},
	}
	return c
}

func TestConfigLoaderParseEnvironment_Success(t *testing.T) {
	name1 := "CFGLOADER_PARSEENV_VAR1"
	value1 := "CFGLOADER_PARSEENV_VAL1"
	name2 := "CFGLOADER_PARSEENV_VAR2"
	name3 := "CFGLOADER_PARSEENV_VAR3"
	name4 := "CFGLOADER_PARSEENV_VAR4"
	input := []environmentNameValuePair{
		{
			Name: name1,
		},
		{
			Name: name2,
			Value: &environmentValue{
				StringValue: new(string),
			},
		},
		{
			Name: name3,
			Value: &environmentValue{
				Int64Value: new(int64),
			},
		},
		{
			Name: name4,
			Value: &environmentValue{
				FloatValue: new(float64),
			},
		},
		{
			Name:  "CFGLOADER_PARSEENV_VAR5",
			Value: &environmentValue{},
		},
		{
			Name: "CFGLOADER_PARSEENV_VAR6",
		},
	}
	c := newTestConfigLoader(map[string]string{
		name1: value1,
	})
	output, err := c.parseEnvironment(input)
	if err != nil {
		t.Error(err)
	}
	if !reflect.DeepEqual(output, map[string]string{
		name1: value1,
		name2: "",
		name3: "0",
		name4: "0",
	}) {
		t.Error(output)
	}
}
func TestConfigLoaderParseEnvironment_InvalidName(t *testing.T) {
	input := []environmentNameValuePair{
		{
			Name: "",
		},
	}
	c := newTestConfigLoader(nil)
	_, err := c.parseEnvironment(input)
	if err == nil {
		t.Fail()
	}
}

func TestConfigLoaderLoadFile_Success(t *testing.T) {
	withMockFS(func() {
		c := newTestConfigLoader(nil)
		cfParsed, err := c.loadFile(testDockerComposeYml)
		if err != nil {
			t.Error(err)
		} else {
			if !cfParsed.version.Equal(v1) {
				t.Fail()
			}
			if len(cfParsed.xProperties) != 0 {
				t.Fail()
			}
			expected := map[string]*composeFileParsedService{
				"testservice": {
					dependsOn: map[string]ServiceHealthiness{},
					service: &Service{
						Command:           []string{"bash", "-c", "echo 'Hello World!'"},
						EntrypointPresent: true,
						Image:             "ubuntu:latest",
						Volumes: []ServiceVolume{
							{
								Short: &PathMapping{
									ContainerPath: "bb",
									HasHostPath:   true,
									HasMode:       true,
									HostPath:      "aa",
									Mode:          "cc",
								},
							},
						},
					},
				},
			}
			assertComposeFileServicesEqual(t, cfParsed.services, expected)
		}
	})
}

func assertComposeFileServicesEqual(t *testing.T, services1, services2 map[string]*composeFileParsedService) {
	if len(services1) != len(services2) {
		t.Fail()
	}
	for name, service1 := range services1 {
		service2 := services2[name]
		if service2 == nil {
			t.Fail()
		} else {
			if len(service1.dependsOn) > 0 || len(service2.dependsOn) > 0 {
				panic("services must not have depends on")
			}
			if service1.extends != nil || service2.extends != nil {
				panic("services must not have extends")
			}
			assertServicesEqual(t, service1.service, service2.service, true)
		}
	}
}

func assertServiceMapsEqual(t *testing.T, services1, services2 map[string]*Service) {
	if len(services1) != len(services2) {
		t.Fail()
	}
	for name, service1 := range services1 {
		service2 := services2[name]
		if service2 == nil {
			t.Fail()
		} else {
			assertServicesEqual(t, service1, service2, true)
			if !areDependsOnMapsEqual(services1, services2, name) {
				t.Logf("dependsOn1: %+v\n", service1.DependsOn)
				t.Logf("dependsOn2: %+v\n", service2.DependsOn)
				t.Fail()
			}
		}
	}
}

func buildNameMap(services map[string]*Service) map[*Service]string {
	names := map[*Service]string{}
	for name, service := range services {
		if _, ok := names[service]; ok {
			panic(errors.New("invalid services map"))
		}
		names[service] = name
	}
	return names
}

func areDependsOnMapsEqual(services1, services2 map[string]*Service, name string) bool {
	dependsOn1 := services1[name].DependsOn
	dependsOn2 := services2[name].DependsOn
	if len(dependsOn1) != len(dependsOn2) {
		return false
	}
	names1 := buildNameMap(services1)
	for service1, healthiness1 := range dependsOn1 {
		name = names1[service1]
		service2 := services2[name]
		if service2 == nil {
			return false
		}
		healthiness2, ok := dependsOn2[service2]
		if !ok || healthiness1 != healthiness2 {
			return false
		}
	}
	return true
}

func assertServicesEqual(t *testing.T, service1, service2 *Service, ignoreDependsOn bool) {
	if service1.Restart != service2.Restart {
		t.Fail()
	}
	if service1.WorkingDir != service2.WorkingDir {
		t.Fail()
	}
	if (service1.User != service2.User) || (service1.User != nil && *service1.User != *service2.User) {
		t.Fail()
	}
	if service1.HealthcheckDisabled != service2.HealthcheckDisabled {
		t.Fail()
	}
	if service1.Healthcheck != nil || service2.Healthcheck != nil {
		t.Fail()
	}
	if !arePortsEqual(service1.Ports, service2.Ports) {
		t.Logf("ports1: %+v\n", service1.Ports)
		t.Logf("ports2: %+v\n", service2.Ports)
		t.Fail()
	}
	assertServicesEqualContinued(t, service1, service2, ignoreDependsOn)
}

func portsIsSubsetOf(ports1, ports2 []PortBinding) bool {
	for _, port1 := range ports1 {
		found := false
		for _, port2 := range ports2 {
			if port1 == port2 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func arePortsEqual(ports1, ports2 []PortBinding) bool {
	if len(ports1) != len(ports2) {
		return false
	}
	return portsIsSubsetOf(ports1, ports2) && portsIsSubsetOf(ports2, ports1)
}

func assertServicesEqualContinued(t *testing.T, service1, service2 *Service, ignoreDependsOn bool) {
	if !areStringMapsEqual(service1.Environment, service2.Environment) {
		t.Logf("env1: %+v\n", service1.Environment)
		t.Logf("env2: %+v\n", service2.Environment)
		t.Fail()
	}
	if service1.EntrypointPresent != service2.EntrypointPresent {
		t.Fail()
	} else if service1.EntrypointPresent && !areStringSlicesEqual(service1.Entrypoint, service2.Entrypoint) {
		t.Logf("entrypoint1: %+v\n", service1.Entrypoint)
		t.Logf("entrypoint2: %+v\n", service2.Entrypoint)
		t.Fail()
	}
	if !areStringSlicesEqual(service1.Command, service2.Command) {
		t.Logf("command1: %+v\n", service1.Command)
		t.Logf("command2: %+v\n", service2.Command)
		t.Fail()
	}
	if !areServiceVolumesEqual(service1.Volumes, service2.Volumes) {
		t.Logf("volumes1: %+v\n", service1.Volumes)
		t.Logf("volumes2: %+v\n", service2.Volumes)
		t.Fail()
	}
	if !ignoreDependsOn && (len(service1.DependsOn) > 0 || len(service2.DependsOn) > 0) {
		panic("services must not have depends on")
	}
}

func areServiceVolumesEqual(volumes1, volumes2 []ServiceVolume) bool {
	n := len(volumes1)
	if n != len(volumes2) {
		return false
	}
	for i := 0; i < n; i++ {
		if !arePathMappingsEqual(volumes1[i].Short, volumes2[i].Short) {
			return false
		}
	}
	return true
}

func arePathMappingsEqual(pm1, pm2 *PathMapping) bool {
	if pm1 == nil {
		return pm2 == nil
	}
	return pm2 != nil && *pm1 == *pm2
}

func areStringMapsEqual(m1, m2 map[string]string) bool {
	if len(m1) != len(m2) {
		return false
	}
	for key, value1 := range m1 {
		value2, ok := m2[key]
		if !ok || value1 != value2 {
			return false
		}
	}
	return true
}

func areStringSlicesEqual(slice1, slice2 []string) bool {
	n := len(slice1)
	if n != len(slice2) {
		return false
	}
	for i := 0; i < n; i++ {
		if slice1[i] != slice2[i] {
			return false
		}
	}
	return true
}

func TestConfigLoaderLoadFile_Error(t *testing.T) {
	withMockFS(func() {
		c := newTestConfigLoader(nil)
		_, err := c.loadFile(testDockerComposeYmlIOError)
		if err == nil {
			t.Fail()
		}
	})
}

func TestConfigLoaderLoadResolvedFile_Caching(t *testing.T) {
	withMockFS(func() {
		c := newTestConfigLoader(nil)
		cfParsed1, err := c.loadResolvedFile(testDockerComposeYml)
		if err != nil {
			t.Error(err)
		}
		cfParsed2, err := c.loadResolvedFile(testDockerComposeYml)
		if err != nil {
			t.Error(err)
		}
		if cfParsed1 != cfParsed2 {
			t.Fail()
		}
	})
}

func TestConfigLoaderLoadResolvedFile_OpenFileError(t *testing.T) {
	withMockFS(func() {
		c := newTestConfigLoader(nil)
		_, err := c.loadResolvedFile(testDockerComposeYmlIOError)
		if err == nil {
			t.Fail()
		}
	})
}

func TestConfigLoaderLoadResolvedFile_VersionError(t *testing.T) {
	withMockFS(func() {
		c := newTestConfigLoader(nil)
		_, err := c.loadResolvedFile(testDockerComposeYmlInvalidVersion)
		if err == nil {
			t.Fail()
		}
	})
}

func TestConfigLoaderLoadResolvedFile_InterpolationError(t *testing.T) {
	withMockFS(func() {
		c := newTestConfigLoader(nil)
		_, err := c.loadResolvedFile(testDockerComposeYmlInterpolationIssue)
		if err == nil {
			t.Fail()
		}
	})
}

func TestConfigLoaderLoadResolvedFile_DecodeError(t *testing.T) {
	withMockFS(func() {
		c := newTestConfigLoader(nil)
		_, err := c.loadResolvedFile(testDockerComposeYmlDecodeIssue)
		if err == nil {
			t.Fail()
		}
	})
}

func TestNew_DependsOnDoesNotExist(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{
			testDockerComposeYmlDependsOnDoesNotExist,
		})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}
func TestNew_DependsOnCycle(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{
			testDockerComposeYmlDependsOnCycle,
		})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}
func TestNew_DependsOn(t *testing.T) {
	withMockFS(func() {
		c, err := New([]string{
			testDockerComposeYmlDependsOn,
		})
		if err != nil {
			t.Error(err)
		} else {
			service1 := &Service{}
			service2 := &Service{}
			service3 := &Service{}
			service1.DependsOn = map[*Service]ServiceHealthiness{
				service2: ServiceStarted,
			}
			service2.DependsOn = map[*Service]ServiceHealthiness{
				service3: ServiceHealthy,
			}
			assertServiceMapsEqual(t, c.Services, map[string]*Service{
				"service1": service1,
				"service2": service2,
				"service3": service3,
			})
		}
	})
}

func TestNew_IOError(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{
			testDockerComposeYmlIOError,
		})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}

func TestNew_MultipleFiles(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{
			testDockerComposeYml,
			testDockerComposeYmlExtends,
		})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}

func TestNew_ExtendsCycle(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{
			testDockerComposeYmlExtendsCycle,
		})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}

func TestNew_ExtendsSuccess(t *testing.T) {
	withMockFS(func() {
		c, err := New([]string{testDockerComposeYmlExtends})
		if err != nil {
			t.Error(err)
		} else {
			assertServiceMapsEqual(t, c.Services, map[string]*Service{
				"service1": {
					Environment: map[string]string{
						"KEY1": "VALUE1",
						"KEY2": "VALUE2",
					},
				},
				"service2": {
					Environment: map[string]string{
						"KEY2": "VALUE2",
					},
				},
				"service3": {},
			})
		}
	})
}

func TestNew_ExtendsIOError(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{testDockerComposeYmlExtendsIOError})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}
func TestNew_ExtendsDoesNotExist(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{testDockerComposeYmlExtendsDoesNotExist})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}
func TestNew_ExtendsDoesNotExistFile(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{testDockerComposeYmlExtendsDoesNotExistFile})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}
func TestNew_ExtendsInvalidDependsOn(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{testDockerComposeYmlExtendsInvalidDependsOn})
		if err == nil {
			t.Fail()
		} else {
			t.Log(err)
		}
	})
}

func TestNew_Success(t *testing.T) {
	withMockFS(func() {
		_, err := New([]string{})
		if err != nil {
			t.Error(err)
		}
	})
}
func TestNew_StandardFileError(t *testing.T) {
	orig := fs.OS
	defer func() {
		fs.OS = orig
	}()
	fs.OS = mockFileSystemStandardFileError
	_, err := New([]string{})
	if err == nil {
		t.Fail()
	}
}

func TestGetVersion_Default(t *testing.T) {
	m := genericMap{}
	v, err := getVersion(m)
	if err != nil {
		t.Error(err)
	}
	if v == nil || !v.Equal(v1) {
		t.Fail()
	}
}

func TestGetVersion_FormatError(t *testing.T) {
	m := genericMap{
		"version": "",
	}
	_, err := getVersion(m)
	if err == nil {
		t.Fail()
	}
}

func TestGetVersion_TypeError(t *testing.T) {
	m := genericMap{
		"version": 0,
	}
	_, err := getVersion(m)
	if err == nil {
		t.Fail()
	}
}

func TestGetVersion_Success(t *testing.T) {
	m := genericMap{
		"version": "1.0",
	}
	v, err := getVersion(m)
	if err != nil {
		t.Error(err)
	}
	if v == nil || !v.Equal(v1) {
		t.Fail()
	}
}

func TestComposeFileParsedServiceClearRecStack_Success(t *testing.T) {
	s := &composeFileParsedService{}
	s.recStack = true
	s.clearRecStack()
	if s.recStack {
		t.Fail()
	}
}

func TestLoadFileError_Success(t *testing.T) {
	err := loadFileError("some file", fmt.Errorf("an error occurred"))
	if err == nil {
		t.Fail()
	}
}

func TestParseComposeFileService_InvalidPortsError(t *testing.T) {
	c := newTestConfigLoader(nil)
	cfService := &composeFileService{
		Ports: []port{
			{
				Value: "asdf",
			},
		},
	}
	_, err := c.parseComposeFileService("", cfService)
	if err == nil {
		t.Fail()
	}
}

func TestParseComposeFileService_InvalidHealthcheckError(t *testing.T) {
	c := newTestConfigLoader(nil)
	cfService := &composeFileService{
		Healthcheck: &ServiceHealthcheck{
			Timeout: util.NewString("henkie"),
		},
	}
	_, err := c.parseComposeFileService("", cfService)
	if err == nil {
		t.Fail()
	}
}

func TestParseComposeFile_InvalidEnvironmentError(t *testing.T) {
	c := newTestConfigLoader(nil)
	cf := &composeFile{
		Services: map[string]*composeFileService{
			"service1": {
				Environment: environment{
					Values: []environmentNameValuePair{
						{
							Name: "",
						},
					},
				},
			},
		},
	}
	cfParsed := &composeFileParsed{}
	err := c.parseComposeFile(cf, cfParsed)
	if err == nil {
		t.Fail()
	} else {
		t.Log(err)
	}
}

func TestGetXProperties_NotGenericMap(t *testing.T) {
	v := getXProperties("")
	if v != nil {
		t.Fail()
	}
}

func TestGetXProperties_Success(t *testing.T) {
	key1 := "x-key1"
	val1 := "val1"
	key2 := "x-key2"
	val2 := "val2"
	v := getXProperties(genericMap{
		key1: val1,
		key2: val2,
	})
	if len(v) != 2 {
		t.Fail()
	}
	if v[key1] != val1 {
		t.Fail()
	}
	if v[key2] != val2 {
		t.Fail()
	}
}
