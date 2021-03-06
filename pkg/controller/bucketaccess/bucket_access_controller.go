/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package bucketaccess

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilversion "k8s.io/apimachinery/pkg/util/version"

	kubeclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/workqueue"

	"github.com/container-object-storage-interface/api/apis/objectstorage.k8s.io/v1alpha1"
	bucketclientset "github.com/container-object-storage-interface/api/clientset"
	"github.com/container-object-storage-interface/api/controller"
	osspec "github.com/container-object-storage-interface/spec"

	"k8s.io/klog"

	"golang.org/x/time/rate"
)

// bucketAccessListener manages BucketAccess objects
type bucketAccessListener struct {
	kubeClient         kubeclientset.Interface
	bucketAccessClient bucketclientset.Interface
	provisionerClient  osspec.ProvisionerClient

	// The name of the provisioner for which this controller handles
	// bucket access.
	provisionerName string
	kubeVersion     *utilversion.Version
}

// NewBucketAccessController returns a controller that manages BucketAccess objects
func NewBucketAccessController(provisionerName string, client osspec.ProvisionerClient) (*controller.ObjectStorageController, error) {
	rateLimit := workqueue.NewMaxOfRateLimiter(
		workqueue.NewItemExponentialFailureRateLimiter(5*time.Second, 60*time.Minute),
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)

	identity := fmt.Sprintf("object-storage-sidecar-%s", provisionerName)
	bc, err := controller.NewObjectStorageController(identity, "bucket-access-controller", 5, rateLimit)
	if err != nil {
		return nil, err
	}

	bal := bucketAccessListener{
		provisionerName:   provisionerName,
		provisionerClient: client,
	}
	bc.AddBucketAccessListener(&bal)

	return bc, nil
}

// InitializeKubeClient initializes the kubernetes client
func (bal *bucketAccessListener) InitializeKubeClient(k kubeclientset.Interface) {
	bal.kubeClient = k

	serverVersion, err := k.Discovery().ServerVersion()
	if err != nil {
		klog.Errorf("unable to get server version: %v", err)
	} else {
		bal.kubeVersion = utilversion.MustParseSemantic(serverVersion.GitVersion)
	}
}

// InitializeBucketClient initializes the object storage bucket client
func (bal *bucketAccessListener) InitializeBucketClient(bc bucketclientset.Interface) {
	bal.bucketAccessClient = bc
}

// Add will call the provisioner to grant permissions
func (bal *bucketAccessListener) Add(ctx context.Context, obj *v1alpha1.BucketAccess) error {
	klog.V(1).Infof("bucketAccessListener: add called for bucket access %s", obj.Name)

	// Verify this bucket access is for this provisioner
	if !strings.EqualFold(obj.Spec.Provisioner, bal.provisionerName) {
		return nil
	}

	req := osspec.ProvisionerGrantBucketAccessRequest{
		BucketName: obj.Spec.BucketInstanceName,
		Principal:  obj.Spec.Principal,
	}

	// TODO set grpc timeout
	rsp, err := bal.provisionerClient.ProvisionerGrantBucketAccess(ctx, &req)
	if err != nil {
		klog.Errorf("error calling ProvisionerGrantBucketAccess: %v", err)
		return err
	}
	klog.V(1).Infof("provisioner returned grant bucket access response %v", rsp)

	// update bucket access status to success
	obj.Status.AccessGranted = true
	_, err = bal.bucketAccessClient.ObjectstorageV1alpha1().BucketAccesses().UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	return nil
}

// Update does nothing
func (bal *bucketAccessListener) Update(ctx context.Context, old, new *v1alpha1.BucketAccess) error {
	klog.V(1).Infof("bucketAccessListener: update called for bucket %s", old.Name)
	return nil
}

// Delete will call the provisioner to revoke permissions
func (bal *bucketAccessListener) Delete(ctx context.Context, obj *v1alpha1.BucketAccess) error {
	klog.V(1).Infof("bucketAccessListener: delete called for bucket access %s", obj.Name)

	// Verify this bucket access is for this provisioner
	if !strings.EqualFold(obj.Spec.Provisioner, bal.provisionerName) {
		return nil
	}

	req := osspec.ProvisionerRevokeBucketAccessRequest{
		BucketName: obj.Spec.BucketInstanceName,
		Principal:  obj.Spec.Principal,
	}

	// TODO set grpc timeout
	rsp, err := bal.provisionerClient.ProvisionerRevokeBucketAccess(ctx, &req)
	if err != nil {
		klog.Errorf("error calling ProvisionerRevokeBucketAccess: %v", err)
		return err
	}
	klog.V(1).Infof("provisioner returned revoke bucket access response %v", rsp)

	return nil
}
