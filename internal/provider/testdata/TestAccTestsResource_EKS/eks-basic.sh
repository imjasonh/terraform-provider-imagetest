#!/bin/sh

set -euxo pipefail

echo "Hello from foo"

kubectl get pods -A

# Exercise upstream EBS driver by installing it and creating a pod with an ephemeral volume.
helm install aws-ebs-csi-driver aws-ebs-csi-driver \
    --repo https://kubernetes-sigs.github.io/aws-ebs-csi-driver \
    --namespace kube-system \
    --wait --timeout=10m

kubectl wait --for=condition=ready pod -n kube-system -l app.kubernetes.io/name=aws-ebs-csi-driver --timeout=5m

kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/aws-ebs-csi-driver/refs/heads/master/examples/kubernetes/ephemeral-volume/manifests/storageclass.yaml

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test
spec:
  containers:
    - name: test
      image: cgr.dev/chainguard/busybox:latest
      command: ["/bin/sh"]
      args: ["-c", "echo $(date -u) >> /data/out.txt"]
      volumeMounts:
        - name: persistent-storage
          mountPath: /data
  volumes:
    - name: persistent-storage
      ephemeral:
        volumeClaimTemplate:
          spec:
            accessModes: [ "ReadWriteOnce" ]
            storageClassName: ebs-ephemeral-demo
            resources:
              requests:
                storage: 1Gi
EOF

# Wait for the pod to be completed
kubectl wait --for=condition=complete pod/test --timeout=5m
