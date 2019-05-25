package up

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/docker/distribution/digestset"
	dockerRef "github.com/docker/distribution/reference"
	dockerTypes "github.com/docker/docker/api/types"
	dockerClient "github.com/docker/docker/client"
	"github.com/jbrekelmans/kube-compose/internal/app/config"
	"github.com/jbrekelmans/kube-compose/internal/app/k8smeta"
	"github.com/jbrekelmans/kube-compose/internal/pkg/docker"
	"github.com/jbrekelmans/kube-compose/internal/pkg/util"
	dockerComposeConfig "github.com/jbrekelmans/kube-compose/pkg/docker/compose/config"
	cmdColor "github.com/logrusorgru/aurora"
	goDigest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	k8sError "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8swatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	clientV1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

var colorSupported = []cmdColor.Color{409600, 147456, 344064, 81920, 212992, 278528, 475136}

type appImageInfo struct {
	err              error
	imageHealthcheck *dockerComposeConfig.Healthcheck
	once             *sync.Once
	podImage         string
	sourceImageID    string
	user             *docker.Userinfo
}

type app struct {
	composeService                       *config.Service
	serviceClusterIP                     string
	imageInfo                            appImageInfo
	maxObservedPodStatus                 podStatus
	containersForWhichWeAreStreamingLogs map[string]bool
	color                                cmdColor.Color
}

func (a *app) name() string {
	return a.composeService.Name
}

func (a *app) hasService() bool {
	return len(a.composeService.Ports) > 0
}

type hostAliases struct {
	v    []v1.HostAlias
	once *sync.Once
	err  error
}

type localImagesCache struct {
	imageIDSet *digestset.Set
	images     []dockerTypes.ImageSummary
	once       *sync.Once
	err        error
}

type upRunner struct {
	apps                  map[string]*app
	appsThatNeedToBeReady map[*app]bool
	appsToBeStarted       map[*app]bool
	cfg                   *config.Config
	completedChannels     []chan interface{}
	dockerClient          *dockerClient.Client
	k8sClientset          *kubernetes.Clientset
	k8sServiceClient      clientV1.ServiceInterface
	k8sPodClient          clientV1.PodInterface
	hostAliases           hostAliases
	localImagesCache      localImagesCache
	maxServiceNameLength  int
	opts                  *Options
}

func (u *upRunner) initKubernetesClientset() error {
	k8sClientset, err := kubernetes.NewForConfig(u.cfg.KubeConfig)
	if err != nil {
		return err
	}
	u.k8sClientset = k8sClientset
	u.k8sServiceClient = u.k8sClientset.CoreV1().Services(u.cfg.Namespace)
	u.k8sPodClient = u.k8sClientset.CoreV1().Pods(u.cfg.Namespace)
	return nil
}

func (u *upRunner) initAppsToBeStarted() {
	u.appsToBeStarted = map[*app]bool{}
	colorIndex := 0
	for _, app := range u.apps {
		if !u.cfg.MatchesFilter(app.composeService) {
			continue
		}
		u.appsToBeStarted[app] = true
		if colorIndex < len(colorSupported) {
			app.color = colorSupported[colorIndex]
			colorIndex++
		} else {
			colorIndex = 0
		}
		if len(app.name()) > u.maxServiceNameLength {
			u.maxServiceNameLength = len(app.name())
		}
	}
}

func (u *upRunner) initApps() {
	u.apps = make(map[string]*app, len(u.cfg.Services))
	u.appsThatNeedToBeReady = map[*app]bool{}
	for _, composeService := range u.cfg.Services {
		app := &app{
			composeService:                       composeService,
			containersForWhichWeAreStreamingLogs: make(map[string]bool),
		}
		app.imageInfo.once = &sync.Once{}
		u.apps[app.name()] = app
	}
}

func (u *upRunner) getAppImageInfo(app *app) error {
	sourceImage := app.composeService.DockerComposeService.Image
	if sourceImage == "" {
		return fmt.Errorf("docker compose service %s has no image or its image is the empty string, and building images is not supported",
			app.name())
	}
	localImageIDSet, err := u.getLocalImageIDSet()
	if err != nil {
		return err
	}
	// Use the same interpretation of images as docker-compose (use ParseAnyReferenceWithSet)
	sourceImageRef, err := dockerRef.ParseAnyReferenceWithSet(sourceImage, localImageIDSet)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("error while parsing image %#v", sourceImage))
	}
	err = u.getAppImageInfoEnsureSourceImageID(sourceImage, sourceImageRef, app, localImageIDSet)
	if err != nil {
		return err
	}
	inspect, inspectRaw, err := u.dockerClient.ImageInspectWithRaw(u.opts.Context, app.imageInfo.sourceImageID)
	if err != nil {
		return err
	}
	err = u.getAppImageEnsureCorrectPodImage(app, sourceImageRef, sourceImage)
	if err != nil {
		return err
	}
	imageHealthcheck, err := inspectImageRawParseHealthcheck(inspectRaw)
	if err != nil {
		return err
	}
	app.imageInfo.imageHealthcheck = imageHealthcheck
	if u.opts.RunAsUser {
		err = u.getAppImageInfoUser(app, &inspect, sourceImage)
	}
	return err
}

func (u *upRunner) getAppImageEnsureCorrectPodImage(a *app, sourceImageRef dockerRef.Reference, sourceImage string) error {
	if u.cfg.PushImages != nil {
		destinationImage := fmt.Sprintf("%s/%s/%s", u.cfg.PushImages.DockerRegistry, u.cfg.Namespace, a.composeService.NameEscaped)
		destinationImagePush := destinationImage + ":latest"
		err := u.dockerClient.ImageTag(u.opts.Context, a.imageInfo.sourceImageID, destinationImagePush)
		if err != nil {
			return err
		}
		var digest string
		digest, err = pushImageWithLogging(u.opts.Context, u.dockerClient, a.name(), destinationImagePush, u.cfg.KubeConfig.BearerToken)
		if err != nil {
			return err
		}
		a.imageInfo.podImage = destinationImage + "@" + digest
	} else if a.imageInfo.podImage == "" {
		_, sourceImageIsNamed := sourceImageRef.(dockerRef.Named)
		if !sourceImageIsNamed {
			// TODO https://github.com/jbrekelmans/kube-compose/issues/6
			return fmt.Errorf("image reference %#v is likely unstable, "+
				"please enable pushing of images or use named image references to improve consistency across hosts", sourceImage)
		}
		a.imageInfo.podImage = sourceImage
	}
	return nil
}

func (u *upRunner) getAppImageInfoEnsureSourceImageID(sourceImage string, sourceImageRef dockerRef.Reference, a *app,
	localImageIDSet *digestset.Set) error {
	// We need the image locally always, so we can parse its healthcheck
	sourceImageNamed, sourceImageIsNamed := sourceImageRef.(dockerRef.Named)
	a.imageInfo.sourceImageID = resolveLocalImageID(sourceImageRef, localImageIDSet, u.localImagesCache.images)
	if !sourceImageIsNamed {
		return fmt.Errorf("could not find image %#v locally, and building images is not supported", sourceImage)
	}
	digest, err := pullImageWithLogging(u.opts.Context, u.dockerClient, a.name(), sourceImageRef.String())
	if err != nil {
		return err
	}
	a.imageInfo.sourceImageID, a.imageInfo.podImage, err = resolveLocalImageAfterPull(
		u.opts.Context, u.dockerClient, sourceImageNamed, digest)
	if err != nil {
		return err
	}
	if a.imageInfo.sourceImageID == "" {
		return fmt.Errorf("could get ID of image %#v, this is either a bug or images were removed by an external process (please try again)",
			sourceImage)
	}
	// len(podImage) > 0 by definition of resolveLocalImageAfterPull
	return nil
}

func (u *upRunner) getAppImageInfoUser(a *app, inspect *dockerTypes.ImageInspect, sourceImage string) error {
	var user *docker.Userinfo
	var err error
	userRaw := a.composeService.DockerComposeService.User
	if userRaw == nil {
		user, err = docker.ParseUserinfo(inspect.Config.User)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("image %#v has an invalid user %#v", sourceImage, inspect.Config.User))
		}
	} else {
		user, err = docker.ParseUserinfo(*userRaw)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("docker-compose service %s has an invalid user %#v", a.name(), *userRaw))
		}
	}
	if user.UID == nil || (user.Group != "" && user.GID == nil) {
		// TODO https://github.com/jbrekelmans/kube-compose/issues/70 confirm whether docker and our pod spec will produce the same default
		// group if a UID is set but no GID
		err := getUserinfoFromImage(u.opts.Context, u.dockerClient, a.imageInfo.sourceImageID, user)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error getting uid/gid from image %#v", sourceImage))
		}
	}
	a.imageInfo.user = user
	return nil
}

func (u *upRunner) getAppImageInfoOnce(app *app) error {
	app.imageInfo.once.Do(func() {
		app.imageInfo.err = u.getAppImageInfo(app)
	})
	return app.imageInfo.err
}

func (u *upRunner) findAppFromObjectMeta(objectMeta *metav1.ObjectMeta) (*app, error) {
	composeService, err := k8smeta.FindFromObjectMeta(u.cfg, objectMeta)
	if err != nil {
		return nil, err
	}
	return u.apps[composeService.Name], err
}

func (u *upRunner) waitForServiceClusterIPUpdate(service *v1.Service) (*app, error) {
	app, err := u.findAppFromObjectMeta(&service.ObjectMeta)
	if err != nil || app == nil {
		return app, err
	}
	if service.Spec.Type != "ClusterIP" {
		return app, k8smeta.ErrorResourcesModifiedExternally()
	}
	app.serviceClusterIP = service.Spec.ClusterIP
	return app, nil
}

func (u *upRunner) waitForServiceClusterIPCountRemaining() int {
	remaining := 0
	for _, app := range u.apps {
		if app.hasService() && app.serviceClusterIP == "" {
			remaining++
		}
	}
	return remaining
}

func (u *upRunner) waitForServiceClusterIPList(expected int, listOptions *metav1.ListOptions) (string, error) {
	serviceList, err := u.k8sServiceClient.List(*listOptions)
	if err != nil {
		return "", err
	}
	if len(serviceList.Items) < expected {
		return "", k8smeta.ErrorResourcesModifiedExternally()
	}
	for i := 0; i < len(serviceList.Items); i++ {
		_, err = u.waitForServiceClusterIPUpdate(&serviceList.Items[i])
		if err != nil {
			return "", err
		}
	}
	return serviceList.ResourceVersion, nil
}

func (u *upRunner) waitForServiceClusterIPWatchEvent(event *k8swatch.Event) error {
	switch event.Type {
	case k8swatch.Added, k8swatch.Modified:
		service := event.Object.(*v1.Service)
		_, err := u.waitForServiceClusterIPUpdate(service)
		if err != nil {
			return err
		}
	case k8swatch.Deleted:
		service := event.Object.(*v1.Service)
		app, err := u.findAppFromObjectMeta(&service.ObjectMeta)
		if err != nil {
			return err
		}
		if app != nil {
			return k8smeta.ErrorResourcesModifiedExternally()
		}
	default:
		return fmt.Errorf("got unexpected error event from channel: %+v", event.Object)
	}
	return nil
}

func (u *upRunner) waitForServiceClusterIPWatch(expected, remaining int, eventChannel <-chan k8swatch.Event) error {
	for {
		event, ok := <-eventChannel
		if !ok {
			return fmt.Errorf("channel unexpectedly closed")
		}
		err := u.waitForServiceClusterIPWatchEvent(&event)
		if err != nil {
			return err
		}
		remainingNew := u.waitForServiceClusterIPCountRemaining()
		if remainingNew != remaining {
			remaining = remainingNew
			fmt.Printf("waiting for cluster IP assignment (%d/%d)\n", expected-remaining, expected)
			if remaining == 0 {
				break
			}
		}
	}
	return nil
}

func (u *upRunner) waitForServiceClusterIP(expected int) error {
	listOptions := metav1.ListOptions{
		LabelSelector: u.cfg.EnvironmentLabel + "=" + u.cfg.EnvironmentID,
	}
	remaining := u.waitForServiceClusterIPCountRemaining()
	resourceVersion, err := u.waitForServiceClusterIPList(expected, &listOptions)
	if err != nil {
		return err
	}
	fmt.Printf("waiting for cluster IP assignment (%d/%d)\n", expected-remaining, expected)
	if remaining == 0 {
		return nil
	}
	listOptions.ResourceVersion = resourceVersion
	listOptions.Watch = true
	watch, err := u.k8sServiceClient.Watch(listOptions)
	if err != nil {
		return err
	}
	defer watch.Stop()
	return u.waitForServiceClusterIPWatch(expected, remaining, watch.ResultChan())
}

func (u *upRunner) createServicesAndGetPodHostAliases() ([]v1.HostAlias, error) {
	expectedServiceCount := 0
	for _, app := range u.apps {
		if !app.hasService() {
			continue
		}
		expectedServiceCount++
		servicePorts := make([]v1.ServicePort, len(app.composeService.DockerComposeService.Ports))
		for i, port := range app.composeService.DockerComposeService.Ports {
			servicePorts[i] = v1.ServicePort{
				Name:       fmt.Sprintf("%s%d", port.Protocol, port.Internal),
				Port:       port.Internal,
				Protocol:   v1.Protocol(strings.ToUpper(port.Protocol)),
				TargetPort: intstr.FromInt(int(port.Internal)),
			}
		}
		service := &v1.Service{
			Spec: v1.ServiceSpec{
				Ports:    servicePorts,
				Selector: k8smeta.InitCommonLabels(u.cfg, app.composeService, nil),
				Type:     v1.ServiceType("ClusterIP"),
			},
		}
		k8smeta.InitObjectMeta(u.cfg, &service.ObjectMeta, app.composeService)
		_, err := u.k8sServiceClient.Create(service)
		switch {
		case k8sError.IsAlreadyExists(err):
			fmt.Printf("app %s: service %s already exists\n", app.name(), service.ObjectMeta.Name)
		case err != nil:
			return nil, err
		default:
			fmt.Printf("app %s: service %s created\n", app.name(), service.ObjectMeta.Name)
		}
	}
	if expectedServiceCount == 0 {
		return nil, nil
	}
	return u.getPodHostAliasesCore(expectedServiceCount)
}

func (u *upRunner) getPodHostAliasesCore(expectedServiceCount int) ([]v1.HostAlias, error) {
	err := u.waitForServiceClusterIP(expectedServiceCount)
	if err != nil {
		return nil, err
	}
	hostAliases := make([]v1.HostAlias, expectedServiceCount)
	i := 0
	for _, app := range u.apps {
		if app.hasService() {
			hostAliases[i] = v1.HostAlias{
				IP: app.serviceClusterIP,
				Hostnames: []string{
					app.name(),
				},
			}
			i++
		}
	}
	return hostAliases, nil
}

func (u *upRunner) initLocalImages() error {
	u.localImagesCache.once.Do(func() {
		imageSummarySlice, err := u.dockerClient.ImageList(u.opts.Context, dockerTypes.ImageListOptions{
			All: true,
		})
		var imageIDSet *digestset.Set
		if err == nil {
			imageIDSet = digestset.NewSet()
			for i := 0; i < len(imageSummarySlice); i++ {
				_ = imageIDSet.Add(goDigest.Digest(imageSummarySlice[i].ID))
			}
		}
		u.localImagesCache = localImagesCache{
			imageIDSet: imageIDSet,
			images:     imageSummarySlice,
			err:        err,
		}
	})
	return u.localImagesCache.err
}

func (u *upRunner) getLocalImageIDSet() (*digestset.Set, error) {
	err := u.initLocalImages()
	if err != nil {
		return nil, err
	}
	return u.localImagesCache.imageIDSet, nil
}

func (u *upRunner) createServicesAndGetPodHostAliasesOnce() ([]v1.HostAlias, error) {
	u.hostAliases.once.Do(func() {
		v, err := u.createServicesAndGetPodHostAliases()
		u.hostAliases.v = v
		u.hostAliases.err = err
	})
	return u.hostAliases.v, u.hostAliases.err
}

func getRestartPolicyforService(app *app) v1.RestartPolicy {
	var restartPolicy v1.RestartPolicy
	switch app.composeService.DockerComposeService.Restart {
	case "no":
		restartPolicy = v1.RestartPolicyNever
	case "always":
		restartPolicy = v1.RestartPolicyAlways
	case "on-failure":
		restartPolicy = v1.RestartPolicyOnFailure
	default:
		restartPolicy = v1.RestartPolicyNever
	}
	return restartPolicy
}

// GetReadinessProbe converts the image/docker-compose healthcheck to a readiness probe to implement depends_on condition: service_healthy
// in docker compose files. Kubernetes does not appear to have disabled the healthcheck of docker images:
// https://stackoverflow.com/questions/41475088/when-to-use-docker-healthcheck-vs-livenessprobe-readinessprobe
// ... so we're not doubling up on healthchecks. We accept that this may lead to calls failing due to removal backend pods from load
// balancers.
func (a *app) GetReadinessProbe() *v1.Probe {
	if !a.composeService.DockerComposeService.HealthcheckDisabled {
		if a.composeService.DockerComposeService.Healthcheck != nil {
			return createReadinessProbeFromDockerHealthcheck(a.composeService.DockerComposeService.Healthcheck)
		} else if a.imageInfo.imageHealthcheck != nil {
			return createReadinessProbeFromDockerHealthcheck(a.imageInfo.imageHealthcheck)
		}
	}
	return nil
}

func (u *upRunner) createPod(app *app) (*v1.Pod, error) {
	err := u.getAppImageInfoOnce(app)
	if err != nil {
		return nil, err
	}
	readinessProbe := app.GetReadinessProbe()

	containerPorts := make([]v1.ContainerPort, len(app.composeService.Ports))
	for i, port := range app.composeService.Ports {
		containerPorts[i] = v1.ContainerPort{
			ContainerPort: port.Port,
			Protocol:      v1.Protocol(strings.ToUpper(port.Protocol)),
		}
	}
	var envVars []v1.EnvVar
	envVarCount := len(app.composeService.DockerComposeService.Environment)
	if envVarCount > 0 {
		envVars = make([]v1.EnvVar, envVarCount)
		i := 0
		for key, value := range app.composeService.DockerComposeService.Environment {
			envVars[i] = v1.EnvVar{
				Name:  key,
				Value: value,
			}
			i++
		}
	}
	hostAliases, err := u.createServicesAndGetPodHostAliasesOnce()
	if err != nil {
		return nil, err
	}

	var securityContext *v1.PodSecurityContext
	if u.opts.RunAsUser {
		securityContext = &v1.PodSecurityContext{
			RunAsUser: app.imageInfo.user.UID,
		}
		if app.imageInfo.user.GID != nil {
			securityContext.RunAsGroup = app.imageInfo.user.GID
		}
	}
	pod := &v1.Pod{
		Spec: v1.PodSpec{
			// new(bool) allocates a bool, sets it to false, and returns a pointer to it.
			AutomountServiceAccountToken: new(bool),
			Containers: []v1.Container{
				{
					Command:         app.composeService.DockerComposeService.Entrypoint,
					Env:             envVars,
					Image:           app.imageInfo.podImage,
					ImagePullPolicy: v1.PullAlways,
					Name:            app.composeService.NameEscaped,
					Ports:           containerPorts,
					ReadinessProbe:  readinessProbe,
					WorkingDir:      app.composeService.DockerComposeService.WorkingDir,
				},
			},
			HostAliases:     hostAliases,
			RestartPolicy:   getRestartPolicyforService(app),
			SecurityContext: securityContext,
		},
	}
	k8smeta.InitObjectMeta(u.cfg, &pod.ObjectMeta, app.composeService)
	podServer, err := u.k8sPodClient.Create(pod)
	if k8sError.IsAlreadyExists(err) {
		fmt.Printf("app %s: pod %s already exists\n", app.name(), pod.ObjectMeta.Name)
	} else if err != nil {
		return nil, err
	}
	u.appsThatNeedToBeReady[app] = true
	return podServer, nil
}

func isPodReady(pod *v1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodReady && condition.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

func parsePodStatus(pod *v1.Pod) (podStatus, error) {
	if isPodReady(pod) {
		return podStatusReady, nil
	}
	runningCount := 0
	for _, containerStatus := range pod.Status.ContainerStatuses {
		t := containerStatus.State.Terminated
		if t != nil {
			return parsePodStatusTerminatedContainer(pod.ObjectMeta.Name, containerStatus.Name, t)
		}
		if w := containerStatus.State.Waiting; w != nil && w.Reason == "ErrImagePull" {
			return podStatusOther, fmt.Errorf("aborting because container %s of pod %s could not pull image: %s",
				containerStatus.Name,
				pod.ObjectMeta.Name,
				w.Message,
			)
		}
		if containerStatus.State.Running != nil {
			runningCount++
		}
	}
	if runningCount == len(pod.Status.ContainerStatuses) {
		return podStatusStarted, nil
	}
	return podStatusOther, nil
}

func parsePodStatusTerminatedContainer(podName, containerName string, t *v1.ContainerStateTerminated) (podStatus, error) {
	if t.Reason != "Completed" {
		return podStatusOther, fmt.Errorf("aborting because container %s of pod %s terminated (code=%d,signal=%d,reason=%s): %s",
			containerName,
			podName,
			t.ExitCode,
			t.Signal,
			t.Reason,
			t.Message,
		)
	}
	return podStatusCompleted, nil
}

func (u *upRunner) updateAppMaxObservedPodStatus(pod *v1.Pod) error {

	app, err := u.findAppFromObjectMeta(&pod.ObjectMeta)
	if err != nil {
		return err
	}
	if app == nil {
		return nil
	}
	// For each container of the pod:
	// 		if the container is running
	//			// use app.containersForWhichWeAreStreamingLogs to determine the following condition
	// 			if we are not already streaming logs for the container
	//				start streaming logs for the container
	if !u.opts.Detach {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			_, ok := app.containersForWhichWeAreStreamingLogs[containerStatus.Name]
			if !ok && containerStatus.State.Running != nil {
				app.containersForWhichWeAreStreamingLogs[containerStatus.Name] = true
				getPodLogOptions := &v1.PodLogOptions{
					Follow:    true,
					Container: containerStatus.Name,
				}
				completedChannel := make(chan interface{})
				u.completedChannels = append(u.completedChannels, completedChannel)
				go u.streamPodLogs(pod, completedChannel, getPodLogOptions, app)
			}
		}
	}
	s, err := parsePodStatus(pod)
	if err != nil {
		return err
	}

	if s > app.maxObservedPodStatus {
		app.maxObservedPodStatus = s
		fmt.Printf("app %s: pod status %s\n", app.name(), &app.maxObservedPodStatus)
	}

	return nil
}

func (u *upRunner) streamPodLogs(pod *v1.Pod, completedChannel chan interface{}, getPodLogOptions *v1.PodLogOptions, app *app) {
	getLogsRequest := u.k8sPodClient.GetLogs(pod.ObjectMeta.Name, getPodLogOptions)
	var bodyReader io.ReadCloser
	bodyReader, err := getLogsRequest.Stream()
	if err != nil {
		panic(err)
	}
	defer util.CloseAndLogError(bodyReader)
	scanner := bufio.NewScanner(bodyReader)
	for scanner.Scan() {
		fmt.Printf("%-*s| %s\n", u.maxServiceNameLength+3, cmdColor.Colorize(app.name, app.color), scanner.Text())
	}
	if err = scanner.Err(); err != nil {
		fmt.Println(err)
	}
	close(completedChannel)
}

func (u *upRunner) createPodsIfNeeded() error {
	for app1 := range u.appsToBeStarted {
		createPod := true
		for dcService, healthiness := range app1.composeService.DockerComposeService.DependsOn {
			composeService := u.cfg.FindService(dcService)
			app2 := u.apps[composeService.Name]
			if healthiness == dockerComposeConfig.ServiceHealthy {
				if app2.maxObservedPodStatus != podStatusReady {
					createPod = false
				}
			} else {
				if app2.maxObservedPodStatus != podStatusStarted {
					createPod = false
				}
			}
		}
		if createPod {
			pod, err := u.createPod(app1)
			if err != nil {
				return err
			}
			reason := u.formatCreatePodReason(app1)
			fmt.Printf("app %s: created pod %s because %s\n", app1.name(), pod.ObjectMeta.Name, reason)
			delete(u.appsToBeStarted, app1)
		}
	}
	return nil
}

func (u *upRunner) formatCreatePodReason(app1 *app) string {
	reason := strings.Builder{}
	reason.WriteString("its dependency conditions are met (")
	comma := false
	for dcService, healthiness := range app1.composeService.DockerComposeService.DependsOn {
		composeService := u.cfg.FindService(dcService)
		if comma {
			reason.WriteString(", ")
		}
		reason.WriteString(composeService.Name)
		if healthiness == dockerComposeConfig.ServiceHealthy {
			reason.WriteString(": ready")
		} else {
			reason.WriteString(": running")
		}
		comma = true
	}
	reason.WriteString(")")
	return reason.String()
}

func (u *upRunner) runStartInitialPods() error {
	for app := range u.appsToBeStarted {
		if len(app.composeService.DockerComposeService.DependsOn) != 0 {
			continue
		}
		pod, err := u.createPod(app)
		if err != nil {
			return err
		}
		fmt.Printf("app %s: created pod %s because all its dependency conditions are met\n", app.name(), pod.ObjectMeta.Name)
		delete(u.appsToBeStarted, app)
	}
	return nil
}

func (u *upRunner) runListPodsAndCreateThemIfNeeded() (string, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: u.cfg.EnvironmentLabel + "=" + u.cfg.EnvironmentID,
	}
	podList, err := u.k8sPodClient.List(listOptions)
	if err != nil {
		return "", err
	}
	for i := 0; i < len(podList.Items); i++ {
		err = u.updateAppMaxObservedPodStatus(&podList.Items[i])
		if err != nil {
			return "", err
		}
	}
	err = u.createPodsIfNeeded()
	if err != nil {
		return "", err
	}
	return podList.ResourceVersion, nil
}

func (u *upRunner) run() error {
	u.initApps()
	u.initAppsToBeStarted()
	err := u.initKubernetesClientset()
	if err != nil {
		return err
	}
	// Initialize docker client
	var dc *dockerClient.Client
	dc, err = dockerClient.NewEnvClient()
	if err != nil {
		return err
	}
	u.dockerClient = dc

	for app := range u.appsToBeStarted {
		// Begin pulling and pushing images immediately...
		// The error returned by getAppImageInfoOnce will be handled later, hence the nolint.
		// nolint
		go u.getAppImageInfoOnce(app)
	}
	// Begin creating services and collecting their cluster IPs (we'll need this to
	// set the hostAliases of each pod).
	// The error returned by getAppImageInfoOnce will be handled later, hence the nolint.
	// nolint
	go u.createServicesAndGetPodHostAliasesOnce()

	err = u.runStartInitialPods()
	if err != nil {
		return err
	}

	var resourceVersion string
	resourceVersion, err = u.runListPodsAndCreateThemIfNeeded()
	if err != nil {
		return err
	}
	err = u.runWatchPods(resourceVersion)
	if err != nil {
		return err
	}
	// Wait for completed channels
	for _, completedChannel := range u.completedChannels {
		<-completedChannel
	}
	return nil
}

func (u *upRunner) runWatchPodsEvent(event *k8swatch.Event) error {
	switch event.Type {
	case k8swatch.Added, k8swatch.Modified:
		pod := event.Object.(*v1.Pod)
		err := u.updateAppMaxObservedPodStatus(pod)
		if err != nil {
			return err
		}
	case k8swatch.Deleted:
		pod := event.Object.(*v1.Pod)
		app, err := u.findAppFromObjectMeta(&pod.ObjectMeta)
		if err != nil {
			return err
		}
		if app != nil {
			return k8smeta.ErrorResourcesModifiedExternally()
		}
	default:
		return fmt.Errorf("got unexpected error event from channel: %+v", event.Object)
	}
	return u.createPodsIfNeeded()
}

func (u *upRunner) runWatchPods(resourceVersion string) error {
	if u.checkIfPodsReady() {
		fmt.Printf("pods ready (%d/%d)\n", len(u.appsThatNeedToBeReady), len(u.appsThatNeedToBeReady))
		return nil
	}
	listOptions := metav1.ListOptions{
		LabelSelector: u.cfg.EnvironmentLabel + "=" + u.cfg.EnvironmentID,
	}
	listOptions.ResourceVersion = resourceVersion
	listOptions.Watch = true
	watch, err := u.k8sPodClient.Watch(listOptions)
	if err != nil {
		return err
	}
	defer watch.Stop()
	eventChannel := watch.ResultChan()
	for {
		event, ok := <-eventChannel
		if !ok {
			return fmt.Errorf("channel unexpectedly closed")
		}
		err = u.runWatchPodsEvent(&event)
		if err != nil {
			return err
		}
		if u.checkIfPodsReady() {
			break
		}
	}
	fmt.Printf("pods ready (%d/%d)\n", len(u.appsThatNeedToBeReady), len(u.appsThatNeedToBeReady))
	return nil
}

func (u *upRunner) checkIfPodsReady() bool {
	allPodsReady := true
	for app := range u.appsThatNeedToBeReady {
		if app.maxObservedPodStatus < podStatusReady {
			allPodsReady = false
		}
	}
	return allPodsReady
}

// Run runs an operation similar docker-compose up against a Kubernetes cluster.
func Run(cfg *config.Config, opts *Options) error {
	// TODO https://github.com/jbrekelmans/kube-compose/issues/2 accept context as a parameter
	u := &upRunner{
		cfg:  cfg,
		opts: opts,
	}
	u.hostAliases.once = &sync.Once{}
	u.localImagesCache.once = &sync.Once{}
	return u.run()
}