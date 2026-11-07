package app

import (
	"context"
	"testing"

	v1alpha1 "github.com/Abraxas-365/odyssey/agent/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// workloadOf is service name of a.
func workloadOf(t *testing.T, a *v1alpha1.OdysseyApp, name string) workload {
	t.Helper()
	for _, w := range workloads(a) {
		if w.Name == name {
			return w
		}
	}
	t.Fatalf("no service %s", name)
	return workload{}
}

// mutated is the Deployment of service name after mutateWorkload over prev
// (nil: a new one).
func mutated(t *testing.T, a *v1alpha1.OdysseyApp, name string, prev *appsv1.Deployment) *appsv1.Deployment {
	t.Helper()
	d := &appsv1.Deployment{}
	if prev != nil {
		d = prev.DeepCopy()
	}
	mutateWorkload(d, a, workloadOf(t, a, name), workloads(a), cfg)
	return d
}

// A database for the api reaches the api only: the web has no DATABASE_URL.
func TestAScopedResourceReachesOnlyItsServices(t *testing.T) {
	a := threeServices(1)
	a.Spec.Resources = []v1alpha1.ResourceRefSpec{{Name: "main", Kind: v1alpha1.ResourcePostgres, Services: []string{"api"}}}
	if _, ok := envOf(mutated(t, a, "api", nil))["DATABASE_URL"]; !ok {
		t.Error("the api reads its database")
	}
	for _, s := range []string{"web", "worker"} {
		d := mutated(t, a, s, nil)
		if _, ok := envOf(d)["DATABASE_URL"]; ok {
			t.Errorf("%s got the api's database", s)
		}
		if _, ok := d.Spec.Template.Annotations[AnnotationResources]; ok {
			t.Errorf("%s rolls with a resource it does not read", s)
		}
	}
	// a resource for every service (no scope) reaches all of them
	a.Spec.Resources[0].Services = nil
	for _, s := range []string{"web", "api", "worker"} {
		if _, ok := envOf(mutated(t, a, s, nil))["DATABASE_URL"]; !ok {
			t.Errorf("%s: an unscoped resource reaches every service", s)
		}
	}
	// a service the app no longer has: the resource reaches nobody
	a.Spec.Resources[0].Services = []string{"gone"}
	for _, s := range []string{"web", "api", "worker"} {
		if _, ok := envOf(mutated(t, a, s, nil))["DATABASE_URL"]; ok {
			t.Errorf("%s: a scope naming a gone service must reach nobody, never everyone", s)
		}
	}
}

func TestScopedSecretsAIAndServiceEnv(t *testing.T) {
	a := threeServices(1)
	a.Spec.Env = map[string]string{"SHARED": "app", "LOG": "info"}
	a.Spec.Services[1].Env = map[string]string{"LOG": "debug", "ONLY_API": "1"}
	a.Spec.Secrets = []v1alpha1.SecretRefSpec{managedRef("ODICEA_AI_KEY", 1), managedRef("STRIPE_KEY", 2)}
	a.Spec.Secrets[1].Services = []string{"api", "worker"}
	a.Spec.AI = &v1alpha1.AISpec{BaseURL: "https://llm.test", KeySecret: "ODICEA_AI_KEY", Services: []string{"worker"}}

	web, api, worker := envOf(mutated(t, a, "web", nil)), envOf(mutated(t, a, "api", nil)), envOf(mutated(t, a, "worker", nil))
	if api["LOG"].Value != "debug" || api["ONLY_API"].Value != "1" || api["SHARED"].Value != "app" {
		t.Errorf("the api's own env wins over the app's: %+v", api)
	}
	if web["LOG"].Value != "info" || web["ONLY_API"].Name != "" {
		t.Errorf("the web keeps the app's env only: %+v", web)
	}
	if web["STRIPE_KEY"].Name != "" || api["STRIPE_KEY"].ValueFrom == nil || worker["STRIPE_KEY"].ValueFrom == nil {
		t.Errorf("STRIPE_KEY reaches api and worker only: web=%+v", web["STRIPE_KEY"])
	}
	if worker[EnvOpenAIKey].ValueFrom == nil || web[EnvOpenAIKey].Name != "" || api[EnvOpenAIKey].Name != "" {
		t.Errorf("the AI reaches the worker only")
	}
	if l := mutated(t, a, "web", nil).Spec.Template.Labels[LabelAI]; l != "" {
		t.Errorf("the web is not an AI pod (egress to the gateway): %q", l)
	}
	if l := mutated(t, a, "worker", nil).Spec.Template.Labels[LabelAI]; l != "true" {
		t.Errorf("the worker is an AI pod: %q", l)
	}
}

// A new version of the api's secret is a new revision: the api rolls, the
// web keeps its pods (same template, same revision label).
func TestChangingTheAPIsSecretDoesNotRestartTheWeb(t *testing.T) {
	a := threeServices(1)
	a.Spec.Secrets = []v1alpha1.SecretRefSpec{managedRef("STRIPE_KEY", 1)}
	a.Spec.Secrets[0].Services = []string{"api"}
	web1, api1 := mutated(t, a, "web", nil), mutated(t, a, "api", nil)

	b := a.DeepCopy()
	b.Spec.Revision = 2
	b.Spec.Labels = map[string]string{"odyssey.dev/deployment": "d2"}
	b.Spec.Secrets[0].Version = 2
	web2, api2 := mutated(t, b, "web", web1), mutated(t, b, "api", api1)

	if revisionOf(web2) != 1 || web2.Spec.Template.Labels["odyssey.dev/deployment"] != "d1" {
		t.Errorf("the web keeps the pods revision 1 made: %v", web2.Spec.Template.Labels)
	}
	if !equalTemplates(web1, web2) {
		t.Error("the web's pod template must not change")
	}
	if revisionOf(api2) != 2 || api2.Spec.Template.Annotations[AnnotationSecrets] == api1.Spec.Template.Annotations[AnnotationSecrets] {
		t.Errorf("the api rolls: %v %v", api2.Spec.Template.Labels, api2.Spec.Template.Annotations)
	}

	// an app-wide change reaches the web again
	c := b.DeepCopy()
	c.Spec.Revision = 3
	c.Spec.Env = map[string]string{"NEW": "1"}
	if web3 := mutated(t, c, "web", web2); revisionOf(web3) != 3 {
		t.Errorf("an env change rolls the web: %v", web3.Spec.Template.Labels)
	}
	// a Deployment of an agent before this (no template hash) rolls as before
	old := web1.DeepCopy()
	delete(old.Annotations, AnnotationTemplate)
	if revisionOf(mutated(t, b, "web", old)) != 2 {
		t.Error("without a recorded hash the Deployment rolls")
	}
}

func equalTemplates(x, y *appsv1.Deployment) bool {
	return templateHash(x.Spec.Template) == templateHash(y.Spec.Template) &&
		revisionOf(x) == revisionOf(y)
}

// A files volume is ReadWriteOnce: only one service mounts it (the first it
// reaches) and only that one is recreated instead of rolled.
func TestAFilesResourceMountsInOneService(t *testing.T) {
	a := threeServices(1)
	a.Spec.Resources = []v1alpha1.ResourceRefSpec{{Name: "uploads", Kind: v1alpha1.ResourceFiles, Services: []string{"worker", "api"}}}
	api, worker := mutated(t, a, "api", nil), mutated(t, a, "worker", nil)
	if len(api.Spec.Template.Spec.Volumes) != 1 || api.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("the api (first in the app's order) mounts it: %+v", api.Spec)
	}
	if len(worker.Spec.Template.Spec.Volumes) != 0 || worker.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		t.Errorf("the worker does not: %+v", worker.Spec)
	}
	if _, ok := envOf(mutated(t, a, "web", nil))["FILES_DIR"]; ok {
		t.Error("the web does not read it")
	}
}

// The reconciler judges a service whose pods were kept by its Deployment:
// the app is ready once the rolled service is, without the kept one's pods
// carrying the new revision.
func TestAKeptServiceDoesNotHoldTheRollout(t *testing.T) {
	ctx := context.Background()
	a := threeServices(1)
	a.Spec.Secrets = []v1alpha1.SecretRefSpec{managedRef("STRIPE_KEY", 1)}
	a.Spec.Secrets[0].Services = []string{"api"}
	r, c := exposed(t, traefikMapper(), a, managedSecret(map[string]int64{"STRIPE_KEY": 1}, map[string]string{"STRIPE_KEY": "v1"}))
	runWith(t, r, c, a)
	becomeReady(t, c, a, "portal-worker")
	runWith(t, r, c, a)
	becomeReady(t, c, a, "portal-web")
	becomeReady(t, c, a, "portal-api")
	if got, _ := runWith(t, r, c, a); got.Status.Phase != v1alpha1.PhaseReady {
		t.Fatalf("revision 1: %+v", got.Status)
	}
	webBefore := deploy(t, c, a, "portal-web")

	var cur v1alpha1.OdysseyApp
	if err := c.Get(ctx, client.ObjectKeyFromObject(a), &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Revision = 2
	cur.Spec.Secrets[0].Version = 2
	if err := c.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	s := managedSecret(map[string]int64{"STRIPE_KEY": 2}, map[string]string{"STRIPE_KEY": "v2"})
	if err := c.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	// the api's new pods are rolling out (the fake client keeps the status)
	rolling := deploy(t, c, a, "portal-api")
	rolling.Status.UpdatedReplicas = 0
	if err := c.Status().Update(ctx, rolling); err != nil {
		t.Fatal(err)
	}
	got, _ := runWith(t, r, c, a)
	web, worker := deploy(t, c, a, "portal-web"), deploy(t, c, a, "portal-worker")
	if revisionOf(web) != 1 || revisionOf(worker) != 1 || web.Generation != webBefore.Generation {
		t.Fatalf("web and worker keep their pods: web %d worker %d", revisionOf(web), revisionOf(worker))
	}
	if revisionOf(deploy(t, c, a, "portal-api")) != 2 {
		t.Fatal("the api rolls")
	}
	if got.Status.Phase == v1alpha1.PhaseReady {
		t.Fatalf("not ready before the api is: %+v", got.Status)
	}
	becomeReady(t, c, a, "portal-api")
	got, _ = runWith(t, r, c, a)
	if got.Status.Phase != v1alpha1.PhaseReady || got.Status.ObservedRevision != 2 || got.Status.ReadyReplicas != 4 {
		t.Fatalf("revision 2 is ready with the kept services: %+v", got.Status)
	}
}
