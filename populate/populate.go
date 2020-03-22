package populate

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/goombaio/namegenerator"
	"github.com/pivotal/kpack/pkg/apis/build/v1alpha1"
	expv1alpha1 "github.com/pivotal/kpack/pkg/apis/experimental/v1alpha1"
	"github.com/pivotal/kpack/pkg/client/clientset/versioned"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/matthewmcnew/build-service-visualization/defaults"
	"github.com/matthewmcnew/build-service-visualization/k8s"
)

func Populate(count int32, order expv1alpha1.Order, imageTag, cacheSize string) error {
	clusterConfig, err := k8s.BuildConfigFromFlags("", "")
	if err != nil {
		return errors.Wrapf(err, "building kubeconfig")
	}

	k8sclient, err := kubernetes.NewForConfig(clusterConfig)
	if err != nil {
		return errors.Wrapf(err, "building kubeconfig")
	}

	client, err := versioned.NewForConfig(clusterConfig)
	if err != nil {
		return errors.Wrapf(err, "building kubeconfig")
	}

	c, err := loadConfig(count, imageTag)
	if err != nil {
		return err
	}

	_, err = k8sclient.CoreV1().Namespaces().Create(&v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaults.Namespace,
		},
	})
	if err != nil && !k8errors.IsAlreadyExists(err) {
		return err
	}

	secret, err := k8sclient.CoreV1().Secrets(defaults.Namespace).Create(&v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pbdemo-dockersecret-",
			Annotations: map[string]string{
				"build.pivotal.io/docker": c.registry,
			},
		},
		StringData: map[string]string{
			"username": c.username,
			"password": c.password,
		},
		Type: v1.SecretTypeBasicAuth,
	})
	if err != nil {
		return err
	}

	serviceAccount, err := k8sclient.CoreV1().ServiceAccounts(defaults.Namespace).Create(&v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pbdemo-serviceaccount-",
		},
		Secrets: []v1.ObjectReference{
			{
				Name: secret.Name,
			},
		},
	})
	if err != nil {
		return err
	}

	err = saveBuilder(client, &expv1alpha1.CustomClusterBuilder{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaults.BuilderName,
		},
		Spec: expv1alpha1.CustomClusterBuilderSpec{
			CustomBuilderSpec: expv1alpha1.CustomBuilderSpec{
				Tag:   fmt.Sprintf("%s:%s", c.imageTag, "builder"),
				Stack: defaults.StackName,
				Store: defaults.StoreName,
				Order: order,
			},
			ServiceAccountRef: v1.ObjectReference{
				Namespace: serviceAccount.Namespace,
				Name:      serviceAccount.Name,
			},
		},
	})
	if err != nil {
		return err
	}

	cache, err := resource.ParseQuantity(cacheSize)
	if err != nil {
		return err
	}

	nameGenerator := namegenerator.NewNameGenerator(time.Now().UTC().UnixNano())
	for i := 1; i <= c.count; i++ {
		sourceConfig, tag := randomSourceConfig()
		image, err := client.BuildV1alpha1().Images(defaults.Namespace).Create(&v1alpha1.Image{
			ObjectMeta: metav1.ObjectMeta{
				Name: nameGenerator.Generate(),
			},
			Spec: v1alpha1.ImageSpec{
				Tag: fmt.Sprintf("%s:%s", c.imageTag, tag),
				Builder: v1.ObjectReference{
					Name: defaults.BuilderName,
					Kind: "CustomClusterBuilder",
				},
				ServiceAccount:       serviceAccount.Name,
				Source:               sourceConfig,
				CacheSize:            &cache,
				ImageTaggingStrategy: v1alpha1.None,
			},
		})
		if err != nil && !k8errors.IsAlreadyExists(err) {
			return err
		} else if k8errors.IsAlreadyExists(err) {
			i--
			continue
		}

		log.Printf("created image %s", image.Name)
		time.Sleep(3 * time.Second)
	}
	return nil
}

type config struct {
	builder  string
	imageTag string
	username string
	password string
	registry string
	count    int
}

func loadConfig(count int32, imageTag string) (config, error) {
	reg, err := name.ParseReference(imageTag, name.WeakValidation)
	if err != nil {
		return config{}, errors.Wrapf(err, "could not parse %s", imageTag)
	}

	auth, err := authn.DefaultKeychain.Resolve(reg.Context().Registry)
	if err != nil {
		return config{}, errors.Wrapf(err, "could not find registry", imageTag)
	}

	basicAuth, err := auth.Authorization()
	if err != nil {
		return config{}, errors.Wrapf(err, "could not get auth for imge", imageTag)
	}

	return config{
		username: basicAuth.Username,
		password: basicAuth.Password,
		count:    int(count),
		imageTag: imageTag,
		registry: func() string {
			if reg.Context().RegistryStr() == name.DefaultRegistry {
				return "https://" + name.DefaultRegistry + "/v1/"
			}
			return reg.Context().RegistryStr()
		}(),
	}, nil
}

func randomSourceConfig() (v1alpha1.SourceConfig, string) {
	rand.Seed(time.Now().UnixNano())
	sourceConfigs := []v1alpha1.SourceConfig{
		{
			Git: &v1alpha1.Git{
				URL:      "https://github.com/matthewmcnew/sample-java-app",
				Revision: "dbba68cee6473b5df51a1a43806d920d2ed4e4ee",
			},
		},
		{
			Git: &v1alpha1.Git{
				URL:      "https://github.com/matthewmcnew/build-samples",
				Revision: "a94df327e098fe924b06547a1adf9c3cda5684c9",
			},
		},
		{
			Git: &v1alpha1.Git{
				URL:      "https://github.com/cloudfoundry/go-mod-cnb",
				Revision: "master",
			},
			SubPath: "integration/testdata/vendored",
		},
		{
			Git: &v1alpha1.Git{
				URL:      "https://github.com/cloudfoundry/dotnet-core-runtime-cnb",
				Revision: "master",
			},
			SubPath: "integration/testdata/simple_app",
		},
	}

	imageTypes := []string{
		"java",
		"node",
		"go",
		"dotnet",
	}

	randomIndex := rand.Intn(len(sourceConfigs))

	return sourceConfigs[randomIndex], imageTypes[randomIndex]
}

func saveBuilder(client *versioned.Clientset, builder *expv1alpha1.CustomClusterBuilder) error {
	existingBuilder, err := client.ExperimentalV1alpha1().CustomClusterBuilders().Get(defaults.BuilderName, metav1.GetOptions{})
	if err != nil && !k8errors.IsNotFound(err) {
		return err
	}
	if k8errors.IsNotFound(err) {
		_, err = client.ExperimentalV1alpha1().CustomClusterBuilders().Create(builder)
	} else {
		oldSpec, err := json.Marshal(existingBuilder.Spec)
		if err != nil {
			return err
		}

		if existingBuilder.Annotations == nil {
			existingBuilder.Annotations = map[string]string{}
		}

		existingBuilder.Annotations[defaults.OldSpecAnnotation] = string(oldSpec)
		existingBuilder.Spec = builder.Spec
		_, err = client.ExperimentalV1alpha1().CustomClusterBuilders().Update(existingBuilder)
	}
	return err
}
