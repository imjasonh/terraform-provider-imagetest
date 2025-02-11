package ekswitheksctl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/chainguard-dev/clog"
	"github.com/chainguard-dev/terraform-provider-imagetest/internal/drivers"
	"github.com/chainguard-dev/terraform-provider-imagetest/internal/drivers/pod"
)

type driver struct {
	name string

	enableEBS bool
	nodes     int32

	region      string
	clusterName string
	namespace   string
	kubeconfig  string
	kcli        kubernetes.Interface
}

type DriverOpts func(*driver) error

func WithEnableEBS(b bool) func(k *driver) error {
	return func(k *driver) error {
		k.enableEBS = b
		return nil
	}
}

func WithNodes(n int32) func(k *driver) error {
	return func(k *driver) error {
		k.nodes = n
		return nil
	}
}

func NewDriver(n string, opts ...DriverOpts) (drivers.Tester, error) {
	k := &driver{
		name:      n,
		region:    "us-west-2",
		namespace: "imagetest",
	}

	for _, s := range []string{"eksctl", "helm"} {
		if _, err := exec.LookPath(s); err != nil {
			return nil, fmt.Errorf("%s not found in $PATH: %w", s, err)
		}
	}

	for _, opt := range opts {
		if err := opt(k); err != nil {
			return nil, err
		}
	}

	return k, nil
}

func (k *driver) eksctl(ctx context.Context, args ...string) error {
	args = append(args, []string{
		"--color", "false", // Disable color output
		"--region", k.region,
	}...)
	clog.FromContext(ctx).Infof("eksctl %v", args)
	cmd := exec.CommandContext(ctx, "eksctl", args...)
	cmd.Env = os.Environ() // Copy the environment
	cmd.Env = append(cmd.Env, "KUBECONFIG="+k.kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("eksctl %v: %v: %s", args, err, out)
	}
	return nil
}

func (k *driver) helm(ctx context.Context, args ...string) error {
	clog.FromContext(ctx).Infof("helm %v", args)
	cmd := exec.CommandContext(ctx, "helm", args...)
	cmd.Env = os.Environ() // Copy the environment
	cmd.Env = append(cmd.Env, "KUBECONFIG="+k.kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm %v: %v: %s", args, err, out)
	}
	return nil
}

func (k *driver) Setup(ctx context.Context) error {
	log := clog.FromContext(ctx)

	if n, ok := os.LookupEnv("IMAGETEST_EKS_CLUSTER"); ok {
		log.Infof("Using cluster name from IMAGETEST_EKS_CLUSTER: %s", n)
		k.clusterName = n
	} else {
		uid := "imagetest-" + uuid.New().String()
		log.Infof("Using random cluster name: %s", uid)
		k.clusterName = uid
	}

	cfg, err := os.Create(filepath.Join(os.TempDir(), k.clusterName))
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	log.Infof("Using kubeconfig: %s", cfg.Name())
	k.kubeconfig = cfg.Name()

	if _, ok := os.LookupEnv("IMAGETEST_EKS_CLUSTER"); ok {
		if err := k.eksctl(ctx, "utils", "write-kubeconfig", "--cluster", k.clusterName, "--kubeconfig", k.kubeconfig); err != nil {
			return fmt.Errorf("eksctl utils write-kubeconfig: %w", err)
		}
	} else {
		if err := k.eksctl(ctx, "create", "cluster",
			"--node-private-networking=false",
			"--vpc-nat-mode=Disable", // Public nodes don't consume NAT gateways
			"--with-oidc",            // Needed to support EBS CSI driver
			"--kubeconfig="+k.kubeconfig,
			"--name="+k.clusterName,
		); err != nil {
			return fmt.Errorf("eksctl create cluster: %w", err)
		}
		log.Infof("Created cluster %s", k.clusterName)

		if err := k.helm(ctx, "install", "aws-observability/amazon-cloudwatch-observability",
			"--repo=https://aws-observability.github.io/helm-charts",
			"--create-namespace", "--namespace", "amazon-cloudwatch",
			"--set", "clusterName="+k.clusterName,
			"--set", "region="+k.region,
			"--wait", "--timeout", "5m",
		); err != nil {
			return fmt.Errorf("helm install amazon-cloudwatch-observability: %w", err)
		}

		if k.enableEBS {
			log.Infof("Enabling EBS support...")
			if err := k.eksctl(ctx, "create", "iamserviceaccount",
				"--name", "ebs-csi-controller-sa",
				"--namespace", "kube-system",
				"--cluster", k.clusterName,
				"--role-name", "AmazonEKS_EBS_CSI_DriverRole",
				"--role-only",
				"--attach-policy-arn", "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy",
				"--approve",
			); err != nil {
				return fmt.Errorf("eksctl create iamserviceaccount: %w", err)
			}
			log.Infof("Created iamserviceaccount ebs-csi-controller-sa")
		}
	}

	// Make sure we write the kubeconfig.
	if err := k.eksctl(ctx, "utils", "write-kubeconfig",
		"--cluster", k.clusterName,
		"--kubeconfig", k.kubeconfig); err != nil {
		return fmt.Errorf("eksctl utils write-kubeconfig: %w", err)
	}
	log.Infof("Wrote kubeconfig for cluster %s to %s", k.clusterName, k.kubeconfig)

	config, err := clientcmd.BuildConfigFromFlags("", k.kubeconfig)
	if err != nil {
		return fmt.Errorf("building kubeconfig: %w", err)
	}

	kcli, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}
	k.kcli = kcli

	return nil
}

func (k *driver) Teardown(ctx context.Context) error {
	if v := os.Getenv("IMAGETEST_EKS_SKIP_TEARDOWN"); v == "true" {
		clog.FromContext(ctx).Info("Skipping EKS teardown due to IMAGETEST_EKS_SKIP_TEARDOWN=true")
		return nil
	}
	if err := k.eksctl(ctx, "delete", "cluster", "--name", k.clusterName); err != nil {
		return fmt.Errorf("eksctl delete cluster: %w", err)
	}
	return nil
}

func (k *driver) Run(ctx context.Context, ref name.Reference) error {
	return pod.Run(ctx, k.kcli,
		pod.WithImageRef(ref),
		pod.WithExtraEnvs(map[string]string{
			"IMAGETEST_DRIVER": "eks_with_eksctl",
		}),
	)
}
