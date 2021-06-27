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
	"voyagermesh.dev/voyager/pkg/eventer"
	"voyagermesh.dev/voyager/pkg/ingress"

	. "gomodules.xyz/x/context"
	core "k8s.io/api/core/v1"
	extensions "k8s.io/api/networking/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	core_util "kmodules.xyz/client-go/core/v1"
	"kmodules.xyz/client-go/meta"
	ext_util "kmodules.xyz/client-go/networking/v1beta1"
	"kmodules.xyz/client-go/tools/queue"
)

func (op *Operator) initIngressWatcher() {
	op.ingInformer = op.kubeInformerFactory.Networking().V1beta1().Ingresses().Informer()
	op.ingQueue = queue.New("Ingress", op.MaxNumRequeues, op.NumThreads, op.reconcileIngress)
	if op.auditor != nil {
		op.ingInformer.AddEventHandler(op.auditor.ForGVK(extensions.SchemeGroupVersion.WithKind("Ingress")))
	}
	op.ingInformer.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			engress, err := api.NewEngressFromIngress(obj.(*extensions.Ingress))
			if err != nil {
				klog.Errorf("Failed to convert Ingress %s/%s into Ingress. Reason %v", engress.Namespace, engress.Name, err)
				return
			}
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
			queue.Enqueue(op.ingQueue.GetQueue(), obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			old, err := api.NewEngressFromIngress(oldObj.(*extensions.Ingress))
			if err != nil {
				klog.Errorf("Failed to convert Ingress %s/%s into Engress. Reason %v", old.Namespace, old.Name, err)
				return
			}
			nu, err := api.NewEngressFromIngress(newObj.(*extensions.Ingress))
			if err != nil {
				klog.Errorf("Failed to convert Ingress %s/%s into Engress. Reason %v", nu.Namespace, nu.Name, err)
				return
			}

			if changed, _ := old.HasChanged(*nu); !changed {
				return
			}
			diff := meta.Diff(old, nu)
			klog.Infof("%s %s/%s has changed. Diff: %s", nu.GroupVersionKind(), nu.Namespace, nu.Name, diff)

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
			queue.Enqueue(op.ingQueue.GetQueue(), newObj)
		},
	})
	op.ingLister = op.kubeInformerFactory.Networking().V1beta1().Ingresses().Lister()
}

func (op *Operator) reconcileIngress(key string) error {
	obj, exists, err := op.ingInformer.GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}
	if !exists {
		klog.Warningf("Ingress %s does not exist anymore\n", key)
		return nil
	}

	ing := obj.(*extensions.Ingress).DeepCopy()
	engress, err := api.NewEngressFromIngress(ing)
	if err != nil {
		klog.Errorf("Failed to convert Ingress %s/%s into Ingress. Reason %v", engress.Namespace, engress.Name, err)
		return nil
	}

	ctrl := ingress.NewController(NewID(context.Background()), op.KubeClient, op.WorkloadClient, op.CRDClient, op.VoyagerClient, op.PromClient, op.svcLister, op.epLister, op.Config, engress, op.recorder)

	if ing.DeletionTimestamp != nil {
		if core_util.HasFinalizer(ing.ObjectMeta, voyager.GroupName) {
			klog.Infof("Delete for engress %s\n", key)
			ctrl.Delete()
			_, _, err = ext_util.PatchIngress(context.TODO(), op.KubeClient, ing, func(obj *extensions.Ingress) *extensions.Ingress {
				obj.ObjectMeta = core_util.RemoveFinalizer(obj.ObjectMeta, voyager.GroupName)
				return obj
			}, metav1.PatchOptions{})
			if err != nil {
				return err
			}
		}
	} else {
		klog.Infof("Sync/Add/Update for ingress %s\n", key)
		if !core_util.HasFinalizer(ing.ObjectMeta, voyager.GroupName) && ctrl.FirewallSupported() {
			_, _, err = ext_util.PatchIngress(context.TODO(), op.KubeClient, ing, func(obj *extensions.Ingress) *extensions.Ingress {
				obj.ObjectMeta = core_util.AddFinalizer(obj.ObjectMeta, voyager.GroupName)
				return obj
			}, metav1.PatchOptions{})
			if err != nil {
				return err
			}
		}
		if core_util.HasFinalizer(ing.ObjectMeta, voyager.GroupName) && !ctrl.FirewallSupported() {
			_, _, err = ext_util.PatchIngress(context.TODO(), op.KubeClient, ing, func(obj *extensions.Ingress) *extensions.Ingress {
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
