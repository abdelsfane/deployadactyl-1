package push

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/compozed/deployadactyl/mocks"
	"github.com/compozed/deployadactyl/randomizer"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/compozed/deployadactyl/creator"
	"github.com/compozed/deployadactyl/interfaces"
	"reflect"
	"github.com/compozed/deployadactyl/state/push"
	"io"
	"errors"
)

var _ = Describe("Service", func() {
	const (
		CONFIGPATH      = "./test_config.yml"
		ENVIRONMENTNAME = "test"
		TESTCONFIG      = `---
environments:
- name: Test
  domain: example.com
  instances: 1
  rollback_enabled: true
  foundations:
  - api1.example.com
  - api2.example.com
  - api3.example.com
  - api4.example.com
`
	)

	var (
		deployadactylServer *httptest.Server
		prechecker *mocks.Prechecker
		eventManager *mocks.EventManager
		provider creator.CreatorModuleProvider

		couriers []*mocks.Courier
		responseBody []byte
		response *http.Response
		org = randomizer.StringRunes(10)
		space = os.Getenv("SILENT_DEPLOY_ENVIRONMENT")
		appName = randomizer.StringRunes(10)
		body io.Reader
	)

	BeforeEach(func() {
		os.Setenv("CF_USERNAME", randomizer.StringRunes(10))
		os.Setenv("CF_PASSWORD", randomizer.StringRunes(10))

		Expect(ioutil.WriteFile(CONFIGPATH, []byte(TESTCONFIG), 0644)).To(Succeed())

		prechecker = &mocks.Prechecker{}
		eventManager = &mocks.EventManager{}
		couriers = make([]*mocks.Courier, 0)

		provider = creator.CreatorModuleProvider{
			NewPrechecker: func(eventManager interfaces.EventManager) interfaces.Prechecker {
				return prechecker
			},
			NewCourier: func(executor interfaces.Executor) interfaces.Courier {
				courier := &mocks.Courier{}
				couriers = append(couriers, courier)
				courier.ExistsCall.Returns.Bool = true
				if len(couriers) == 2 {
					courier.PushCall.Returns.Error = errors.New("failed to push")
				}

				return courier
			},
			NewEventManager: func(log interfaces.Logger) interfaces.EventManager {
				return eventManager
			},
		}

		creator, err := creator.Custom("DEBUG", CONFIGPATH, provider)

		Expect(err).ToNot(HaveOccurred())

		controller := creator.CreateController()
		deployadactylHandler := creator.CreateControllerHandler(controller)

		deployadactylServer = httptest.NewServer(deployadactylHandler)

		body, err = os.Open("../fixtures/artifact-with-manifest.jar")
		Expect(err).ToNot(HaveOccurred())

		requestURL := fmt.Sprintf("%s/v3/apps/%s/%s/%s/%s", deployadactylServer.URL, ENVIRONMENTNAME, org, space, appName)
		req, err := http.NewRequest("POST", requestURL, body)
		Expect(err).ToNot(HaveOccurred())

		req.Header.Add("Content-Type", "application/zip")

		client := &http.Client{}

		response, err = client.Do(req)
		Expect(err).ToNot(HaveOccurred())

		responseBody, err = ioutil.ReadAll(response.Body)
		response.Body.Close()
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		deployadactylServer.Close()
	})

	It("returns correct status code", func() {
		Expect(response.StatusCode).To(Equal(http.StatusInternalServerError), string(responseBody))
	})
	It("calls prechecker with all foundation urls", func() {
		fs := prechecker.AssertAllFoundationsUpCall.Received.Environment.Foundations
		Expect(fs).To(Equal([]string{"api1.example.com", "api2.example.com", "api3.example.com", "api4.example.com"}))
	})
	It("creates correct number of courier objects", func() {
		Expect(len(couriers)).To(Equal(4))
	})
	It("calls courier push with correct info", func() {
		for _, c := range couriers {
			Expect(c.PushCall.Received.AppPath).To(ContainSubstring("/deployadactyl-"))
			Expect(c.PushCall.Received.AppName).To(ContainSubstring(appName+"-new-build-"))
			Expect(c.PushCall.Received.Instances).To(Equal(uint16(1)))
			Expect(c.PushCall.Received.Hostname).To(Equal(appName))
		}
	})
	It("calls courier login with correct info", func() {
		for _, c := range couriers {
			Expect(c.LoginCall.Received.Username).To(Equal(os.Getenv("CF_USERNAME")))
			Expect(c.LoginCall.Received.Password).To(Equal(os.Getenv("CF_PASSWORD")))
			Expect(c.LoginCall.Received.Org).To(Equal(org))
			Expect(c.LoginCall.Received.Space).To(Equal(space))
			Expect(c.LoginCall.Received.SkipSSL).To(Equal(false))
		}
	})
	It("calls courier login with correct foundation url", func() {
		furls := []string{"api1.example.com", "api2.example.com", "api3.example.com", "api4.example.com"}
		for i, c := range couriers {
			Expect(c.LoginCall.Received.FoundationURL).To(Equal(furls[i]))
		}
	})
	It("checks for prior existence of the app", func() {
		for _, c := range couriers {
			Expect(c.ExistsCall.Received.AppName).To(Equal(appName))
		}
	})
	It("maps the new application routes", func() {
		for i, c := range couriers {
			if i != 1 {
				Expect(len(c.MapRouteCall.Received.AppName)).To(Equal(1))
				Expect(c.MapRouteCall.Received.AppName[0]).To(ContainSubstring(appName + "-new-build-"))
				Expect(c.MapRouteCall.Received.Domain[0]).To(Equal("example.com"))
				Expect(c.MapRouteCall.Received.Hostname[0]).To(Equal(appName))
			}
		}
	})
	It("deletes the new application", func() {
		for _, c := range couriers {
			Expect(c.DeleteCall.Received.AppName).To(ContainSubstring(appName+"-new-build-"))
		}
	})
	It("calls Emit the correct number of times", func() {
		Expect(len(eventManager.EmitCall.Received.Events)).To(Equal(7))
	})
	It("emits a deploy.start event", func() {
		Expect(eventManager.EmitCall.Received.Events[0].Type).To(Equal("deploy.start"))
	})
	It("emits a push.started event", func() {
		Expect(eventManager.EmitCall.Received.Events[1].Type).To(Equal("push.started"))
	})
	It("emits a push.finished event", func() {
		Expect(eventManager.EmitCall.Received.Events[2].Type).To(Equal("push.finished"))
		Expect(eventManager.EmitCall.Received.Events[3].Type).To(Equal("push.finished"))
		Expect(eventManager.EmitCall.Received.Events[4].Type).To(Equal("push.finished"))
	})
	It("emits a deploy.failure event", func() {
		Expect(eventManager.EmitCall.Received.Events[5].Type).To(Equal("deploy.failure"))
	})
	It("emits a deploy.finish event", func() {
		Expect(eventManager.EmitCall.Received.Events[6].Type).To(Equal("deploy.finish"))
	})
	It("calls EmitEvent the correct number of times", func() {
		Expect(len(eventManager.EmitEventCall.Received.Events)).To(Equal(9))
	})
	It("emits a DeployStartedEvent", func() {
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[0])).To(Equal(reflect.TypeOf(push.DeployStartedEvent{})))
	})
	It("emits a ArtifactRetrievalStartEvent", func() {
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[1])).To(Equal(reflect.TypeOf(push.ArtifactRetrievalStartEvent{})))
	})
	It("emits a ArtifactRetrievalSuccessEvent", func() {
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[2])).To(Equal(reflect.TypeOf(push.ArtifactRetrievalSuccessEvent{})))
	})
	It("emits a PushStartedEvent", func() {
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[3])).To(Equal(reflect.TypeOf(push.PushStartedEvent{})))
	})
	It("emits a PushFinishedEvent for each foundation", func() {
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[4])).To(Equal(reflect.TypeOf(push.PushFinishedEvent{})))
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[5])).To(Equal(reflect.TypeOf(push.PushFinishedEvent{})))
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[6])).To(Equal(reflect.TypeOf(push.PushFinishedEvent{})))
	})
	It("emits a DeployFailureEvent", func() {
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[7])).To(Equal(reflect.TypeOf(push.DeployFailureEvent{})))
	})
	It("emits a DeployFinishedEvent", func() {
		Expect(reflect.TypeOf(eventManager.EmitEventCall.Received.Events[8])).To(Equal(reflect.TypeOf(push.DeployFinishedEvent{})))
	})
})
