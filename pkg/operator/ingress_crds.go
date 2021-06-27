/*
Copyright AppsCode Inc. and Contributors

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

package operator

import (
	"context"

	"voyagermesh.dev/voyager/apis/voyager"
	api "voyagermesh.dev/voyager/apis/voyager/v1beta1"
	"voyagermesh.dev/voyager/client/clientset/versioned/typed/voyager/v1beta1/util"
	"voyagermesh.dev/voyager/pkg/eventer"
	"voyagermesh.dev/voyager/pkg/ingress"

	. "gomodules.xyz/x/context"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	core_util "kmodules.xyz/client-go/core/v1"
	"kmodules.xyz/client-go/meta"
	"kmodules.xyz/client-go/tools/queue"
)

func (op *Operator) initIngressCRDWatcher() {
	op.engInformer = op.voyagerInformerFactory.Voyager().V1beta1().Ingresses().Informer()
	op.engQueue = queue.New("IngressCRD", op.MaxNumRequeues, op.NumThreads, op.reconcileEngress)
	if op.auditor != nil {
		op.engInformer.AddEventHandler(op.auditor.ForGVK(api.SchemeGroupVersion.WithKind(api.ResourceKindIngress)))
	}
	op.engInformer.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			engress := obj.(*api.Ingress).DeepCopy()
			engress.Migrate()

			if err := engress.IsValid(op.CloudProvider); err != nil {
				op.recorder.Eventf(
					engress.ObjectReference(),
					core.EventTypeWarning,
					eventer.EventReasonIngressInvalid,
					"Reason: %s",
					err.Error(),
				)
				return
			}
			queue.Enqueue(op.engQueue.GetQueue(), obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			old := oldObj.(*api.Ingress).DeepCopy()
			old.Migrate()

			nu := newObj.(*api.Ingress).DeepCopy()
			nu.Migrate()

			if changed, _ := old.HasChanged(*nu); !changed {
				return
			}
			diff := meta.Diff(old, nu)
			klog.Infof("%s %s/%s has changed. Diff: %s", nu.APISchema(), nu.Namespace, nu.Name, diff)

			if err := nu.IsValid(op.CloudProvider); err != nil {
				op.recorder.Eventf(
					nu.ObjectReference(),
					core.EventTypeWarning,
					eventer.EventReasonIngressInvalid,
					"Reason: %s",
					err.Error(),
				)
				return
			}
			queue.Enqueue(op.engQueue.GetQueue(), newObj)
		},
	})
	op.engLister = op.voyagerInformerFactory.Voyager().V1beta1().Ingresses().Lister()
}

func (op *Operator) reconcileEngress(key string) error {
	obj, exists, err := op.engInformer.GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}
	if !exists {
		klog.Warningf("Engress %s does not exist anymore\n", key)
		return nil
	}

	engress := obj.(*api.Ingress).DeepCopy()
	engress.Migrate()
	ctrl := ingress.NewController(NewID(context.Background()), op.KubeClient, op.WorkloadClient, op.CRDClient, op.VoyagerClient, op.PromClient, op.svcLister, op.epLister, op.Config, engress, op.recorder)

	if engress.DeletionTimestamp != nil {
		if core_util.HasFinalizer(engress.ObjectMeta, voyager.GroupName) {
			klog.Infof("Delete for engress %s\n", key)
			ctrl.Delete()
			_, _, err = util.PatchIngress(context.TODO(), op.VoyagerClient.VoyagerV1beta1(), engress, func(obj *api.Ingress) *api.Ingress {
				obj.ObjectMeta = core_util.RemoveFinalizer(obj.ObjectMeta, voyager.GroupName)
				return obj
			}, metav1.PatchOptions{})
			if err != nil {
				return err
			}
		}
	} else {
		klog.Infof("Sync/Add/Update for engress %s\n", key)
		if !core_util.HasFinalizer(engress.ObjectMeta, voyager.GroupName) && ctrl.FirewallSupported() {
			_, _, err = util.PatchIngress(context.TODO(), op.VoyagerClient.VoyagerV1beta1(), engress, func(obj *api.Ingress) *api.Ingress {
				obj.ObjectMeta = core_util.AddFinalizer(obj.ObjectMeta, voyager.GroupName)
				return obj
			}, metav1.PatchOptions{})
			if err != nil {
				return err
			}
		}
		if core_util.HasFinalizer(engress.ObjectMeta, voyager.GroupName) && !ctrl.FirewallSupported() {
			_, _, err = util.PatchIngress(context.TODO(), op.VoyagerClient.VoyagerV1beta1(), engress, func(obj *api.Ingress) *api.Ingress {
				obj.ObjectMeta = core_util.RemoveFinalizer(obj.ObjectMeta, voyager.GroupName)
				return obj
			}, metav1.PatchOptions{})
			if err != nil {
				return err
			}
		}
		if engress.ShouldHandleIngress(op.IngressClass) {
			return ctrl.Reconcile()
		} else {
			klog.Infof("%s %s/%s does not match ingress class", engress.APISchema(), engress.Namespace, engress.Name)
			ctrl.Delete()
		}
	}
	return nil
}
