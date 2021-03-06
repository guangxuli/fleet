package git

import (
	"context"
	"time"

	fleet "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	"github.com/rancher/fleet/pkg/config"
	fleetcontrollers "github.com/rancher/fleet/pkg/generated/controllers/fleet.cattle.io/v1alpha1"
	gitjob "github.com/rancher/gitjob/pkg/apis/gitjob.cattle.io/v1"
	v1 "github.com/rancher/gitjob/pkg/generated/controllers/gitjob.cattle.io/v1"
	"github.com/rancher/wrangler/pkg/apply"
	"github.com/rancher/wrangler/pkg/name"
	"github.com/rancher/wrangler/pkg/relatedresource"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func Register(ctx context.Context, apply apply.Apply, gitJobs v1.GitJobController, gitRepos fleetcontrollers.GitRepoController) {
	h := &handler{
		gitjobCache: gitJobs.Cache(),
	}

	fleetcontrollers.RegisterGitRepoGeneratingHandler(ctx, gitRepos, apply, "", "gitjobs", h.OnChange, nil)
	relatedresource.Watch(ctx, "gitjobs",
		relatedresource.OwnerResolver(true, fleet.SchemeGroupVersion.String(), "GitRepo"), gitRepos, gitJobs)
}

type handler struct {
	gitjobCache v1.GitJobCache
}

func (h *handler) OnChange(gitrepo *fleet.GitRepo, status fleet.GitRepoStatus) ([]runtime.Object, fleet.GitRepoStatus, error) {
	dirs := gitrepo.Spec.BundleDirs
	if len(dirs) == 0 {
		dirs = []string{"."}
	}

	gitJob, err := h.gitjobCache.Get(gitrepo.Namespace, gitrepo.Name)
	if err == nil {
		status.Commit = gitJob.Status.Commit
		status.Conditions = gitJob.Status.Conditions
	} else {
		status.Commit = ""
		status.Conditions = nil
	}

	branch, rev := gitrepo.Spec.Branch, gitrepo.Spec.Revision
	if branch == "" && rev == "" {
		branch = "master"
	}

	saName := name.SafeConcatName("git", gitrepo.Name)
	return []runtime.Object{
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: gitrepo.Namespace,
			},
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: gitrepo.Namespace,
			},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:     []string{"get", "create", "update"},
					APIGroups: []string{"fleet.cattle.io"},
					Resources: []string{"bundles"},
				},
				{
					Verbs:     []string{"get"},
					APIGroups: []string{"fleet.cattle.io"},
					Resources: []string{"gitrepos"},
				},
			},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: gitrepo.Namespace,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      saName,
					Namespace: gitrepo.Namespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     saName,
			},
		},
		&gitjob.GitJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      gitrepo.Name,
				Namespace: gitrepo.Namespace,
			},
			Spec: gitjob.GitJobSpec{
				Git: gitjob.GitInfo{
					Credential: gitjob.Credential{
						GitSecretName: gitrepo.Spec.ClientSecretName,
						GitHostname:   "github.com",
					},
					Provider: "polling",
					Repo:     gitrepo.Spec.Repo,
					Revision: rev,
					Branch:   branch,
				},
				JobSpec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							CreationTimestamp: metav1.Time{Time: time.Unix(0, 0)},
						},
						Spec: corev1.PodSpec{
							ServiceAccountName: saName,
							RestartPolicy:      corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								{
									Name:            "fleet",
									Image:           config.Get().AgentImage,
									ImagePullPolicy: corev1.PullPolicy(config.Get().AgentImagePullPolicy),
									Command: append([]string{
										"fleet",
										"apply",
										"--label=fleet.cattle.io/repo-name=" + gitrepo.Name,
										"--namespace", gitrepo.Namespace,
										"--service-account", gitrepo.Spec.ServiceAccount,
										gitrepo.Name,
									}, dirs...),
									WorkingDir: "/workspace/source",
								},
							},
						},
					},
				},
			},
		},
	}, status, nil
}
