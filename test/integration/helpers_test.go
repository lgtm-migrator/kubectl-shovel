//go:build integration
// +build integration

package integration_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8s "k8s.io/client-go/kubernetes"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/dodopizza/kubectl-shovel/internal/flags"
	"github.com/dodopizza/kubectl-shovel/internal/globals"
	"github.com/dodopizza/kubectl-shovel/internal/kubernetes"
)

var (
	namespace            = "default"
	sidecarContainerName = "sidecar"
	targetPodNamePrefix  = "sample-app"
	targetContainerName  = "target"
)

var (
	DumperImage           = "kubectl-shovel/dumper-integration-tests"
	TargetContainerImage  = "kubectl-shovel/sample-integration-tests"
	SidecarContainerImage = "gcr.io/google_containers/pause:3.1"
)

type TestCase struct {
	name       string
	args       []string
	pod        *core.Pod
	output     string
	hostOutput bool
}

func NewTestCase(name string) *TestCase {
	return &TestCase{name: name, args: []string{}, pod: singleContainerPod(), hostOutput: true}
}

func (tc *TestCase) WithPod(pod *core.Pod) *TestCase {
	tc.pod = pod
	return tc
}

func (tc *TestCase) DownloadOutput() *TestCase {
	tc.hostOutput = false
	return tc
}

func (tc *TestCase) WithArgs(args ...string) *TestCase {
	if len(args)%2 != 0 {
		panic(fmt.Errorf("length of args must be divided by two"))
	}
	tc.args = append(tc.args, args...)
	return tc
}

func (tc *TestCase) FormatArgs(command string) []string {
	args := flags.NewArgs().
		AppendRaw(command).
		Append("pod-name", tc.pod.Name).
		Append("image", DumperImage)

	if tc.hostOutput {
		args.AppendKey("store-output-on-host")
	} else {
		args.Append("output", tc.output)
	}

	for key := 0; key < len(tc.args); key += 2 {
		value := key + 1
		args.Append(tc.args[key], tc.args[value])
	}

	return args.Get()
}

func newTestKubeClient() *kubernetes.Client {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = filepath.Join(homedir.HomeDir(), ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	clientset, err := k8s.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	return &kubernetes.Client{
		Namespace: namespace,
		Clientset: clientset,
	}
}

func testSetup(t *testing.T, command string) func() {
	t.Helper()
	t.Parallel()

	dir := filepath.Join(os.TempDir(), globals.PluginName, command)
	t.Logf("Create directory (%s) for command (%s) tests outputs\n", dir, command)
	_ = os.MkdirAll(dir, os.ModePerm)

	return func() {
		t.Helper()
		t.Logf("Remove directory (%s) for command (%s) tests outputs\n", dir, command)
		_ = os.Remove(dir)
	}
}

func testCaseSetup(t *testing.T, tc *TestCase, command string) func() {
	t.Helper()
	k := newTestKubeClient()

	t.Log("Deploying target pod to cluster...")
	_, err := k.CoreV1().Pods(namespace).Create(
		context.Background(),
		tc.pod,
		meta.CreateOptions{},
	)
	require.NoError(t, err)

	t.Log("Waiting target pod to start...")
	_, err = k.WaitPod(tc.pod.ObjectMeta.Labels)
	require.NoError(t, err)

	if !tc.hostOutput {
		parent := filepath.Join(os.TempDir(), globals.PluginName, command)
		dir, _ := ioutil.TempDir(parent, "*")
		tc.output = filepath.Join(dir, "output")
		t.Logf("Output for test case will be stored at: %s\n", tc.output)
	}

	return func() {
		t.Helper()
		t.Logf("Delete test pod: %s\n", tc.pod.Name)

		policy := meta.DeletePropagationForeground
		_ = k.CoreV1().Pods(namespace).Delete(
			context.TODO(),
			tc.pod.Name,
			meta.DeleteOptions{PropagationPolicy: &policy},
		)
	}
}

func generateRandomPodMeta() meta.ObjectMeta {
	name := fmt.Sprintf("%s-%s", targetPodNamePrefix, uuid.NewString())

	return meta.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			"app": name,
		},
	}
}

func targetContainer() core.Container {
	return core.Container{
		Name:            targetContainerName,
		Image:           TargetContainerImage,
		ImagePullPolicy: core.PullIfNotPresent,
		Ports: []core.ContainerPort{{
			ContainerPort: 6000,
			Name:          "app",
			Protocol:      "TCP",
		}},
		LivenessProbe: &core.Probe{
			ProbeHandler: core.ProbeHandler{
				HTTPGet: &core.HTTPGetAction{
					Path: "/health/live",
					Port: intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "app",
					},
					Scheme: "HTTP",
				},
			},
			InitialDelaySeconds: 2,
			TimeoutSeconds:      1,
			PeriodSeconds:       1,
			SuccessThreshold:    1,
			FailureThreshold:    5,
		},
		TerminationMessagePolicy: core.TerminationMessageFallbackToLogsOnError,
	}
}

func sidecarContainer() core.Container {
	return core.Container{
		Name:  sidecarContainerName,
		Image: SidecarContainerImage,
	}
}

func singleContainerPod() *core.Pod {
	return &core.Pod{
		ObjectMeta: generateRandomPodMeta(),
		Spec: core.PodSpec{
			Containers: []core.Container{targetContainer()},
		},
	}
}

func multiContainerPod() *core.Pod {
	return &core.Pod{
		ObjectMeta: generateRandomPodMeta(),
		Spec: core.PodSpec{
			Containers: []core.Container{targetContainer(), sidecarContainer()},
		},
	}
}

func multiContainerPodWithDefaultContainer() *core.Pod {
	objectMeta := generateRandomPodMeta()
	objectMeta.Annotations = map[string]string{
		"kubectl.kubernetes.io/default-container": targetContainerName,
	}
	return &core.Pod{
		ObjectMeta: objectMeta,
		Spec: core.PodSpec{
			Containers: []core.Container{targetContainer(), sidecarContainer()},
		},
	}
}

func multiContainerPodWithSharedMount() *core.Pod {
	volumes := []core.Volume{
		{
			Name: "shared-path-to-tmp",
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
	}
	mounts := []core.VolumeMount{
		{
			Name:      "shared-path-to-tmp",
			MountPath: globals.PathTmpFolder,
		},
	}

	sidecar := sidecarContainer()
	sidecar.VolumeMounts = mounts

	target := targetContainer()
	target.VolumeMounts = mounts

	return &core.Pod{
		ObjectMeta: generateRandomPodMeta(),
		Spec: core.PodSpec{
			Containers: []core.Container{target, sidecar},
			Volumes:    volumes,
		},
	}
}

func cases(additional ...*TestCase) []*TestCase {
	basic := []*TestCase{
		NewTestCase("Basic test with output on host"),
		NewTestCase("Basic test with downloading output").
			DownloadOutput(),
		NewTestCase("MultiContainer pod").
			WithPod(multiContainerPod()).
			WithArgs("container", targetContainerName).
			DownloadOutput(),
		NewTestCase("MultiContainer pod with default-container annotation").
			WithPod(multiContainerPodWithDefaultContainer()).
			DownloadOutput(),
		NewTestCase("MultiContainer pod with shared mount").
			WithPod(multiContainerPodWithSharedMount()).
			WithArgs("container", targetContainerName).
			DownloadOutput(),
	}

	return append(basic, additional...)
}
