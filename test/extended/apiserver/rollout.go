package apiserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/origin/test/extended/single_node"
	exutil "github.com/openshift/origin/test/extended/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eskipper "k8s.io/kubernetes/test/e2e/framework/skipper"
)

const desiredTestDuration = 1 * time.Hour

var _ = ginkgo.Describe("[Conformance][Suite:openshift/kube-apiserver/rollout][Jira:\"kube-apiserver\"][sig-kube-apiserver] kube-apiserver", func() {
	f := framework.NewDefaultFramework("rollout-resiliency")
	f.SkipNamespaceCreation = true

	oc := exutil.NewCLIWithoutNamespace("rollout-resiliency")

	ginkgo.It("should roll out new revisions without disruption [apigroup:config.openshift.io][apigroup:operator.openshift.io]", func() {
		ctx := context.Background()

		// separate context so we exit our loop, but it is still possible to use the main context for client calls
		shouldEndTestCtx, shouldEndCancelFn := context.WithTimeout(ctx, desiredTestDuration)
		defer shouldEndCancelFn()

		controlPlaneTopology, _ := single_node.GetTopologies(f)
		if controlPlaneTopology == configv1.SingleReplicaTopologyMode {
			e2eskipper.Skipf("SNO always faces disruption on restart")
		}

		operatorClient := oc.AdminOperatorClient()

		kasStatus, err := operatorClient.OperatorV1().KubeAPIServers().Get(ctx, "cluster", metav1.GetOptions{})
		framework.ExpectNoError(err)
		previousLatestRevision := kasStatus.Status.LatestAvailableRevision - 1

		errs := []error{}
		for i := 1; i < 1000; i++ { // we exit early when our desired duration finishes, but this gives us a nice counter for output.
			if shouldEndTestCtx.Err() != nil {
				break
			}

			// ensure the kube-apiserver operator is stable
			nextLogTime := time.Now().Add(time.Minute)
			for {
				rolloutNumberWaitForStability := i - 1

				// prevent hot loops, the extra delay doesn't really matter
				time.Sleep(10 * time.Second)
				if shouldEndTestCtx.Err() != nil {
					break
				}

				// this may actually be flaky if the kube-apiserver is rolling out badly.  Keep track of failures so we can
				// fail the run, but don't exit the test here.
				kasStatus, err := operatorClient.OperatorV1().KubeAPIServers().Get(ctx, "cluster", metav1.GetOptions{})
				if err != nil {
					errs = append(errs, fmt.Errorf("failed reading clusteroperator, run=%d, time=%v, err=%w", i, time.Now(), err))
					continue
				}

				// check to see that every node is at the latest revision
				latestRevision := kasStatus.Status.LatestAvailableRevision
				if latestRevision <= previousLatestRevision {
					framework.Logf("kube-apiserver still has not observed rollout %d: previousLatestRevision=%d, latestRevision=%d", rolloutNumberWaitForStability, previousLatestRevision, latestRevision)
					continue
				}

				nodeNotAtRevisionReasons := []string{}
				for _, nodeStatus := range kasStatus.Status.NodeStatuses {
					if nodeStatus.CurrentRevision != latestRevision {
						nodeNotAtRevisionReasons = append(nodeNotAtRevisionReasons, fmt.Sprintf("node/%v is at revision %d, not %d", nodeStatus.NodeName, nodeStatus.CurrentRevision, latestRevision))
					}
				}
				if len(nodeNotAtRevisionReasons) == 0 {
					break
				}
				if time.Now().After(nextLogTime) {
					framework.Logf("kube-apiserver still not stable after rollout %d: %v", rolloutNumberWaitForStability, strings.Join(nodeNotAtRevisionReasons, "; "))
					nextLogTime = time.Now().Add(time.Minute)
				}
			}
			if shouldEndTestCtx.Err() != nil {
				break
			}

			kasStatus, err := operatorClient.OperatorV1().KubeAPIServers().Get(ctx, "cluster", metav1.GetOptions{})
			framework.ExpectNoError(err)
			previousLatestRevision = kasStatus.Status.LatestAvailableRevision // our next command will increment it.

			framework.Logf("Forcing API rollout %d", i)
			ginkgo.By(fmt.Sprintf("Forcing API rollout %d", i))
			forceKubeAPIServerRollout(ctx, operatorClient, fmt.Sprintf("rollout %d-", i))
		}

		if len(errs) > 0 {
			framework.ExpectNoError(errors.Join(errs...))
		}
	})

})
