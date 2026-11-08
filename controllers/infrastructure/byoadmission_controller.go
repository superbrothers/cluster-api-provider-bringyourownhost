// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"slices"
	"strings"

	infrav1 "github.com/superbrothers/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	clientset "k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// hostCSRApprovedReason is the reason of the Approved condition.
	hostCSRApprovedReason = "Approved by ByoAdmission Controller"
	// hostCSRInvalidReason is the reason of the Denied condition.
	hostCSRInvalidReason = "ByohCSRInvalid"
	// bootstrapUsernamePrefix is the prefix of the username of a bootstrap
	// token, followed by the token ID.
	bootstrapUsernamePrefix = "system:bootstrap:"
	// maxValueLength is the length to truncate the values of a CSR to in the
	// messages and the logs, as the requester controls them.
	maxValueLength = 64
)

var (
	// oidOrganization and oidCommonName are the types of the attributes of
	// the subject that a CSR of an agent has.
	oidOrganization = asn1.ObjectIdentifier{2, 5, 4, 10}
	oidCommonName   = asn1.ObjectIdentifier{2, 5, 4, 3}
)

// hostCSRUsages are the sets of the usages that a CSR of an agent may request:
// client authentication, with the key usages that the kubelet also requests.
var hostCSRUsages = [][]certv1.KeyUsage{
	{certv1.UsageClientAuth},
	{certv1.UsageDigitalSignature, certv1.UsageClientAuth},
	{certv1.UsageDigitalSignature, certv1.UsageKeyEncipherment, certv1.UsageClientAuth},
}

// ByoAdmissionReconciler reconciles a ByoAdmission object
type ByoAdmissionReconciler struct {
	ClientSet clientset.Interface
}

//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;list;watch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kube-apiserver-client,verbs=approve

// Reconcile approves the CSRs of the agents, and denies the other CSRs whose
// names have the prefix of the agents.
func (r *ByoAdmissionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err error
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received", "object", req.NamespacedName)

	// Fetch the CSR from the api-server
	csr, err := r.ClientSet.CertificatesV1().CertificateSigningRequests().Get(ctx, req.NamespacedName.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "CertificateSigningRequest not found, won't reconcile")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Check if the CSR is already approved or denied
	csrApproved := checkCSRCondition(csr.Status.Conditions, certv1.CertificateApproved)
	csrDenied := checkCSRCondition(csr.Status.Conditions, certv1.CertificateDenied)
	if csrApproved || csrDenied {
		if csrApproved {
			logger.Info("CertificateSigningRequest is already approved", "CSR", csr.Name)
		}
		if csrDenied {
			logger.Info("CertificateSigningRequest is already denied", "CSR", csr.Name)
		}
		return ctrl.Result{}, nil
	}

	// Denying a CSR also needs the permission to approve for its signer, which
	// the manager has only for the signer of the agents. Leave the other CSRs
	// to their approvers.
	if csr.Spec.SignerName != certv1.KubeAPIServerClientSignerName {
		logger.Info("Ignoring CertificateSigningRequest for another signer", "CSR", csr.Name, "signerName", truncate(csr.Spec.SignerName))
		return ctrl.Result{}, nil
	}

	condition := certv1.CertificateSigningRequestCondition{
		Type:   certv1.CertificateApproved,
		Status: corev1.ConditionTrue,
		Reason: hostCSRApprovedReason,
	}
	if invalid := validateHostCSR(csr); invalid != nil {
		condition = certv1.CertificateSigningRequestCondition{
			Type:    certv1.CertificateDenied,
			Status:  corev1.ConditionTrue,
			Reason:  hostCSRInvalidReason,
			Message: invalid.Error(),
		}
	}
	csr.Status.Conditions = append(csr.Status.Conditions, condition)

	logger.Info("Updating the approval of CSR", "object", req.NamespacedName, "condition", condition.Type, "message", condition.Message)
	_, err = r.ClientSet.CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	if err != nil {
		return reconcile.Result{}, err
	}

	logger.Info("Updated the approval of CSR", "object", req.NamespacedName, "condition", condition.Type)

	return ctrl.Result{}, nil
}

// validateHostCSR returns an error that tells why the CSR is not one that an
// agent creates to register its host. The signer of the agents issues a
// certificate of any subject that the approver approves, so the subject and
// the requester have to be checked here.
func validateHostCSR(csr *certv1.CertificateSigningRequest) error {
	if csr.Spec.SignerName != certv1.KubeAPIServerClientSignerName {
		return fmt.Errorf("the signer must be %s, not %q", certv1.KubeAPIServerClientSignerName, truncate(csr.Spec.SignerName))
	}
	if !slices.ContainsFunc(hostCSRUsages, func(usages []certv1.KeyUsage) bool {
		return sameUsages(usages, csr.Spec.Usages)
	}) {
		return fmt.Errorf("the usages must be one of %v, not %s", hostCSRUsages, truncate(fmt.Sprint(csr.Spec.Usages)))
	}
	namespace, hostName, err := validateHostCSRRequest(csr.Spec.Request)
	if err != nil {
		return err
	}
	// One CSR per host: the name of the CSR is made of the namespace and the
	// name in the common name, as the agent names it.
	if name := infrav1.HostCSRName(namespace, hostName); csr.Name != name {
		return fmt.Errorf("the name of the CSR must be %q for the common name, not %q", truncate(name), truncate(csr.Name))
	}

	// An agent registers its host with a bootstrap token of BYOH. It renews
	// its certificate this way too for now; renewing with its own certificate
	// will need a check that the requester is the host of the common name.
	if !slices.Contains(csr.Spec.Groups, infrav1.BootstrapTokenExtraGroups) || !strings.HasPrefix(csr.Spec.Username, bootstrapUsernamePrefix) {
		return fmt.Errorf("the requester %q must be a bootstrap token in the group %q",
			truncate(csr.Spec.Username), infrav1.BootstrapTokenExtraGroups)
	}
	return nil
}

// validateHostCSRRequest returns the namespace and the host name in the
// common name of the certificate request of the CSR, or an error that tells
// why the request is not one of a host.
//
// The namespace in the common name is not checked against the bootstrap token
// that requests the certificate, so a bootstrap token of BYOH can get a
// certificate for any namespace until the tokens are bound to the hosts.
func validateHostCSRRequest(pemRequest []byte) (namespace, hostName string, err error) {
	block, _ := pem.Decode(pemRequest)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return "", "", fmt.Errorf("the request must be a PEM-encoded CERTIFICATE REQUEST")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse the request: %w", err)
	}
	if err := request.CheckSignature(); err != nil {
		return "", "", fmt.Errorf("the signature of the request is invalid: %w", err)
	}

	subject := request.Subject
	if !slices.Equal(subject.Organization, []string{infrav1.HostsGroup}) {
		return "", "", fmt.Errorf("the organization must be only %q, not %s", infrav1.HostsGroup, truncate(fmt.Sprintf("%q", subject.Organization)))
	}
	identity, ok := strings.CutPrefix(subject.CommonName, infrav1.HostUsernamePrefix)
	if ok {
		namespace, hostName, ok = strings.Cut(identity, ":")
	}
	if !ok {
		return "", "", fmt.Errorf("the common name must be %s<namespace>:<host name>, not %q", infrav1.HostUsernamePrefix, truncate(subject.CommonName))
	}
	if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
		return "", "", fmt.Errorf("the namespace %q in the common name is invalid: %s", truncate(namespace), strings.Join(errs, ", "))
	}
	if errs := validation.IsDNS1123Subdomain(hostName); len(errs) > 0 {
		return "", "", fmt.Errorf("the host name %q in the common name is invalid: %s", truncate(hostName), strings.Join(errs, ", "))
	}
	// The subject must have no other attributes, such as an organizational
	// unit or a user ID, nor a second common name, as the API server or other
	// authenticators of the certificate may read them.
	if !hasOnlyOrganizationAndCommonName(subject.Names) {
		return "", "", fmt.Errorf("the subject must have only one organization and one common name, not %d attributes", len(subject.Names))
	}
	if len(request.DNSNames) > 0 || len(request.IPAddresses) > 0 || len(request.EmailAddresses) > 0 || len(request.URIs) > 0 {
		return "", "", fmt.Errorf("the request must not have subject alternative names")
	}
	return namespace, hostName, nil
}

// hasOnlyOrganizationAndCommonName returns whether the attributes of a subject
// are exactly one organization and one common name.
func hasOnlyOrganizationAndCommonName(names []pkix.AttributeTypeAndValue) bool {
	if len(names) != 2 { //nolint:mnd
		return false
	}
	var organizations, commonNames int
	for _, attribute := range names {
		switch {
		case attribute.Type.Equal(oidOrganization):
			organizations++
		case attribute.Type.Equal(oidCommonName):
			commonNames++
		}
	}
	return organizations == 1 && commonNames == 1
}

// truncate returns the value cut to maxValueLength runes, so that a long value
// in a CSR does not make the messages and the logs too long.
func truncate(value string) string {
	runes := []rune(value)
	if len(runes) <= maxValueLength {
		return value
	}
	return string(runes[:maxValueLength]) + "..."
}

// sameUsages returns whether the two lists have the same usages, in any order.
func sameUsages(a, b []certv1.KeyUsage) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

// Check if the CSR has the given condition.
func checkCSRCondition(conditions []certv1.CertificateSigningRequestCondition, conditionType certv1.RequestConditionType) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoAdmissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&certv1.CertificateSigningRequest{}).WithEventFilter(
		// watch only BYOH created CSRs
		predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return strings.HasPrefix(e.Object.GetName(), infrav1.HostCSRNamePrefix)
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return strings.HasPrefix(e.ObjectOld.GetName(), infrav1.HostCSRNamePrefix)
			}}).
		Complete(r)
}
