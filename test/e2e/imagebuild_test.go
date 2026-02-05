package e2e_test

import (
	"context"
	"fmt"
	"os"

	buildv1alpha1 "github.com/dana-team/image-builder-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	shipwright "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newClient() client.Client {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(buildv1alpha1.AddToScheme(s))
	utilruntime.Must(shipwright.AddToScheme(s))

	c, err := client.New(cfg, client.Options{Scheme: s})
	Expect(err).NotTo(HaveOccurred())
	return c
}

func newImageBuild(namespace, revision string) *buildv1alpha1.ImageBuild {
	return &buildv1alpha1.ImageBuild{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "imagebuild-",
			Namespace:    namespace,
		},
		Spec: buildv1alpha1.ImageBuildSpec{
			BuildFile: buildv1alpha1.ImageBuildFile{
				Mode: buildv1alpha1.ImageBuildFileModeAbsent,
			},
			Source: buildv1alpha1.ImageBuildSource{
				Type: buildv1alpha1.ImageBuildSourceTypeGit,
				Git: buildv1alpha1.ImageBuildGitSource{
					URL:      "https://github.com/dana-team/image-builder-operator",
					Revision: revision,
				},
			},
			Output: buildv1alpha1.ImageBuildOutput{
				Image: "registry.example.com/team/imagebuild-e2e:v1",
			},
		},
	}
}

var _ = Describe("ImageBuild Shipwright integration", func() {
	var (
		ctx            context.Context
		c              client.Client
		namespace      string
		imageBuildName string
	)

	BeforeEach(func() {
		ctx = context.Background()
		c = newClient()

		By("Creating test namespace")
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-e2e-"}}
		Expect(c.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name

		By("Ensuring Shipwright is installed and reachable")
		Expect(c.List(ctx, &shipwright.BuildRunList{}, client.InNamespace(namespace))).To(Succeed())

		By("Ensuring build policy and strategy exist")
		policy := &buildv1alpha1.ImageBuildPolicy{}
		Expect(c.Get(ctx, types.NamespacedName{Name: imageBuildPolicyName}, policy)).To(Succeed())
		absentStrategy := policy.Spec.ClusterBuildStrategy.BuildFile.Absent
		Expect(c.Get(ctx, types.NamespacedName{Name: absentStrategy}, &shipwright.ClusterBuildStrategy{})).To(Succeed())

		By("Creating an ImageBuild")
		ib := newImageBuild(namespace, "rev-1")
		Expect(c.Create(ctx, ib)).To(Succeed())
		Expect(ib.Name).NotTo(BeEmpty())
		imageBuildName = ib.Name
	})

	AfterEach(func() {
		if os.Getenv("E2E_SKIP_CLEANUP") != "" {
			return
		}
		By("Deleting test namespace")
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
		Expect(client.IgnoreNotFound(c.Delete(ctx, ns))).To(Succeed())
	})

	It("creates Shipwright Build and BuildRun", func() {
		By("Waiting for ImageBuild to report the created Build and BuildRun names")
		buildName := waitForBuildRef(ctx, c, namespace, imageBuildName)
		buildRunName := waitForLastBuildRunRef(ctx, c, namespace, imageBuildName)

		By("Waiting for the Shipwright Build")
		_ = waitForBuild(ctx, c, namespace, imageBuildName, buildName)

		By("Waiting for the Shipwright BuildRun")
		_ = waitForBuildRun(ctx, c, namespace, imageBuildName, buildRunName, buildName)
	})

	It("creates a new BuildRun on ImageBuild update", func() {
		By("Reading the Build ref (stable across the update)")
		buildName := waitForBuildRef(ctx, c, namespace, imageBuildName)

		By("Waiting for the initial BuildRun")
		initialBuildRunName := waitForLastBuildRunRef(ctx, c, namespace, imageBuildName)

		By("Verifying the initial BuildRun exists before updating")
		_ = waitForBuildRun(ctx, c, namespace, imageBuildName, initialBuildRunName, buildName)

		By("Updating ImageBuild to trigger a new BuildRun")
		updateImageBuild(ctx, c, namespace, imageBuildName, "rev-2")

		By("Waiting for ImageBuild to point to a new BuildRun")
		newBuildRunName := waitForBuildRunChange(ctx, c, namespace, imageBuildName, initialBuildRunName)

		By("Waiting for the new Shipwright BuildRun")
		waitForBuildRun(ctx, c, namespace, imageBuildName, newBuildRunName, buildName)
	})

	It("deletes owned Shipwright resources when ImageBuild is deleted", func() {
		By("Waiting for initial Shipwright resources")
		buildName := waitForBuildRef(ctx, c, namespace, imageBuildName)
		buildRunName1 := waitForLastBuildRunRef(ctx, c, namespace, imageBuildName)
		build := waitForBuild(ctx, c, namespace, imageBuildName, buildName)
		buildRun1 := waitForBuildRun(ctx, c, namespace, imageBuildName, buildRunName1, buildName)

		By("Updating ImageBuild to create a second BuildRun")
		updateImageBuild(ctx, c, namespace, imageBuildName, "rev-2")
		buildRunName2 := waitForBuildRunChange(ctx, c, namespace, imageBuildName, buildRunName1)
		buildRun2 := waitForBuildRun(ctx, c, namespace, imageBuildName, buildRunName2, buildName)

		By("Deleting the ImageBuild")
		imageBuild := &buildv1alpha1.ImageBuild{}
		Expect(c.Get(ctx, types.NamespacedName{Name: imageBuildName, Namespace: namespace}, imageBuild)).To(Succeed())
		Expect(c.Delete(ctx, imageBuild)).To(Succeed())

		By("Verifying GC deletes the owned Shipwright resources")
		for _, obj := range []client.Object{build, buildRun1, buildRun2} {
			Eventually(func() bool {
				err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj)
				return apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		}
	})
})

func waitForBuildRef(ctx context.Context, c client.Client, namespace, imageBuildName string) string {
	var buildName string
	Eventually(func(g Gomega) {
		imageBuild := &buildv1alpha1.ImageBuild{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: imageBuildName, Namespace: namespace}, imageBuild)).To(Succeed())
		g.Expect(imageBuild.Status.BuildRef).NotTo(BeEmpty())
		buildName = imageBuild.Status.BuildRef
	}, timeout, interval).Should(Succeed())
	return buildName
}

func waitForLastBuildRunRef(ctx context.Context, c client.Client, namespace, imageBuildName string) string {
	var buildRunName string
	Eventually(func(g Gomega) {
		imageBuild := &buildv1alpha1.ImageBuild{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: imageBuildName, Namespace: namespace}, imageBuild)).To(Succeed())
		g.Expect(imageBuild.Status.LastBuildRunRef).NotTo(BeEmpty())
		buildRunName = imageBuild.Status.LastBuildRunRef
	}, timeout, interval).Should(Succeed())
	return buildRunName
}

func waitForBuild(
	ctx context.Context, c client.Client, namespace, imageBuildName, buildName string,
) *shipwright.Build {
	var build *shipwright.Build
	Eventually(func(g Gomega) {
		imageBuild := &buildv1alpha1.ImageBuild{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: imageBuildName, Namespace: namespace}, imageBuild)).To(Succeed())

		b := &shipwright.Build{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: buildName, Namespace: namespace}, b)).To(Succeed())
		g.Expect(metav1.IsControlledBy(b, imageBuild)).To(BeTrue())
		build = b
	}, timeout, interval).Should(Succeed())
	return build
}

func waitForBuildRun(
	ctx context.Context, c client.Client, namespace, imageBuildName, buildRunName, buildName string,
) *shipwright.BuildRun {
	var buildRun *shipwright.BuildRun
	Eventually(func(g Gomega) {
		imageBuild := &buildv1alpha1.ImageBuild{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: imageBuildName, Namespace: namespace}, imageBuild)).To(Succeed())

		br := &shipwright.BuildRun{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: buildRunName, Namespace: namespace}, br)).To(Succeed())
		g.Expect(metav1.IsControlledBy(br, imageBuild)).To(BeTrue())
		g.Expect(br.Spec.Build.Name).NotTo(BeNil())
		g.Expect(*br.Spec.Build.Name).To(Equal(buildName))
		buildRun = br
	}, timeout, interval).Should(Succeed())
	return buildRun
}

func updateImageBuild(ctx context.Context, c client.Client, namespace, imageBuildName, revision string) {
	backoff := retry.DefaultRetry
	backoff.Steps = 10
	err := retry.RetryOnConflict(backoff, func() error {
		imageBuild := &buildv1alpha1.ImageBuild{}
		if err := c.Get(ctx, types.NamespacedName{Name: imageBuildName, Namespace: namespace}, imageBuild); err != nil {
			return fmt.Errorf("failed to get ImageBuild %s/%s: %w", namespace, imageBuildName, err)
		}
		imageBuild.Spec.Source.Git.Revision = revision
		return c.Update(ctx, imageBuild)
	})
	Expect(err).NotTo(HaveOccurred())
}

func waitForBuildRunChange(
	ctx context.Context, c client.Client, namespace, imageBuildName, previousBuildRunName string,
) string {
	var newBuildRunName string
	Eventually(func(g Gomega) {
		imageBuild := &buildv1alpha1.ImageBuild{}
		g.Expect(c.Get(ctx, types.NamespacedName{Name: imageBuildName, Namespace: namespace}, imageBuild)).To(Succeed())
		g.Expect(imageBuild.Status.LastBuildRunRef).NotTo(Equal(previousBuildRunName))
		newBuildRunName = imageBuild.Status.LastBuildRunRef
	}, timeout, interval).Should(Succeed())
	return newBuildRunName
}
