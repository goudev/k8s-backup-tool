package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"
)

func main() {
	// Configuração do cliente dentro de um pod
	config, err := rest.InClusterConfig()
	if err != nil {
		// Caso esteja rodando fora do cluster, configure o kubeconfig
		kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			panic(err.Error())
		}
	}

	// Cria o cliente dinâmico
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// Cria o cliente estático
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// Obtém a lista de namespaces da variável de ambiente
	namespaces := os.Getenv("NAMESPACES")
	var namespaceList []string

	if namespaces == "*" {
		fmt.Println("Capturando todos os namespaces...")
		namespaceList, err = getAllNamespaces(clientset)
		if err != nil {
			panic(fmt.Sprintf("Erro ao obter a lista de namespaces: %v", err))
		}
	} else {
		namespaceList = strings.Split(namespaces, ",")
	}

	// Cria o diretório base para armazenar os arquivos YAML, caso não exista
	baseOutputDir := "resources"
	err = os.MkdirAll(baseOutputDir, os.ModePerm)
	if err != nil {
		panic(err.Error())
	}

	// Define os subdiretórios para armazenar as versões originais e modificadas dos arquivos YAML
	originalDir := filepath.Join(baseOutputDir, "original")
	modifiedDir := filepath.Join(baseOutputDir, "modified")

	// Cria os diretórios de saída para as versões originais e modificadas
	for _, dir := range []string{originalDir, modifiedDir} {
		err = os.MkdirAll(dir, os.ModePerm)
		if err != nil {
			panic(fmt.Sprintf("Erro ao criar diretório %s: %v", dir, err))
		}
	}

	// Defina os tipos de recursos que você deseja capturar
	resourceTypes := []schema.GroupVersionResource{
		{Group: "apps", Version: "v1", Resource: "deployments"},
		{Group: "apps", Version: "v1", Resource: "daemonsets"},
		{Group: "apps", Version: "v1", Resource: "statefulsets"},
		{Group: "batch", Version: "v1", Resource: "jobs"},
		{Group: "batch", Version: "v1", Resource: "cronjobs"},
		{Group: "", Version: "v1", Resource: "pods"},
		{Group: "", Version: "v1", Resource: "services"},
		{Group: "", Version: "v1", Resource: "configmaps"},
		{Group: "", Version: "v1", Resource: "secrets"},
		{Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
		// Adicionando Istio VirtualService, Gateway, DestinationRule, e EnvoyFilter
		{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"},
		{Group: "networking.istio.io", Version: "v1", Resource: "gateways"},
		{Group: "networking.istio.io", Version: "v1", Resource: "destinationrules"},
		{Group: "networking.istio.io", Version: "v1alpha3", Resource: "envoyfilters"},
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
		{Group: "", Version: "v1", Resource: "serviceaccounts"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},
		{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"},
		{Group: "", Version: "v1", Resource: "persistentvolumes"},
		{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"},
		{Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"},
		{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
		{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
	}

	// Adicionar o tipo de recurso Namespace separadamente
	namespaceResourceType := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}

	// Itera sobre cada namespace
	for _, namespace := range namespaceList {
		namespace = strings.TrimSpace(namespace)
		fmt.Printf("Processando namespace: %s\n", namespace)

		// Cria diretórios para o namespace nas versões original e modificada
		originalNamespaceDir := filepath.Join(originalDir, namespace)
		modifiedNamespaceDir := filepath.Join(modifiedDir, namespace)

		for _, dir := range []string{originalNamespaceDir, modifiedNamespaceDir} {
			err = os.MkdirAll(dir, os.ModePerm)
			if err != nil {
				fmt.Printf("Erro ao criar diretório para namespace %s: %v\n", namespace, err)
				continue
			}
		}

		// Salvar o próprio recurso Namespace
		err = saveNamespaceResource(dynClient, namespace, originalNamespaceDir, modifiedNamespaceDir, namespaceResourceType)
		if err != nil {
			fmt.Printf("Erro ao salvar o recurso Namespace %s: %v\n", namespace, err)
		}

		// Itera sobre cada tipo de recurso
		for _, res := range resourceTypes {
			fmt.Printf("Processando tipo de recurso: %s\n", res.Resource)

			resourceList, err := dynClient.Resource(res).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				fmt.Printf("Erro ao listar recursos %s no namespace %s: %v\n", res.Resource, namespace, err)
				continue
			}

			if len(resourceList.Items) == 0 {
				fmt.Printf("Nenhum recurso do tipo %s encontrado no namespace %s.\n", res.Resource, namespace)
				continue
			}

			// Cria diretórios para o tipo de recurso nas versões original e modificada
			originalResourceDir := filepath.Join(originalNamespaceDir, res.Resource)
			modifiedResourceDir := filepath.Join(modifiedNamespaceDir, res.Resource)

			for _, dir := range []string{originalResourceDir, modifiedResourceDir} {
				err = os.MkdirAll(dir, os.ModePerm)
				if err != nil {
					fmt.Printf("Erro ao criar diretório para recurso %s: %v\n", res.Resource, err)
					continue
				}
			}

			// Salva cada recurso como um arquivo YAML
			for _, item := range resourceList.Items {
				originalFilePath := filepath.Join(originalResourceDir, item.GetName()+".yaml")
				modifiedFilePath := filepath.Join(modifiedResourceDir, item.GetName()+".yaml")
				fmt.Printf("Salvando recurso: %s\n", originalFilePath)

				// Salva o recurso original como YAML
				err = saveResourceAsYAML(&item, originalFilePath)
				if err != nil {
					fmt.Printf("Erro ao salvar recurso original %s: %v\n", item.GetName(), err)
				}

				// Remove campos indesejados para a versão modificada
				cleanResource(&item)

				// Salva o recurso modificado como YAML
				err = saveResourceAsYAML(&item, modifiedFilePath)
				if err != nil {
					fmt.Printf("Erro ao salvar recurso modificado %s: %v\n", item.GetName(), err)
				}
			}
		}
	}

	fmt.Println("Processamento concluído.")

	// Gerar nome do arquivo zip com a data atual
	currentDate := time.Now().Format("2006-01-02") // Formato: AAAA-MM-DD
	zipFileName := fmt.Sprintf("backup-cluster-%s.zip", currentDate)

	// Compacta o diretório de recursos
	err = ZipDirectory(baseOutputDir, zipFileName)
	if err != nil {
		fmt.Printf("Erro ao compactar diretório: %v\n", err)
	} else {
		fmt.Println("Compactação concluída com sucesso.")
	}

	// URL do Pre-Authenticated Request (PAR)
	parURL := os.Getenv("OCI_PAR_URL")

	// Certifique-se de que a URL do PAR termina com o nome do arquivo
	if !strings.HasSuffix(parURL, "/"+zipFileName) {
		parURL = strings.TrimSuffix(parURL, "/") + "/" + zipFileName
	}

	// Faz upload do arquivo compactado usando o PAR
	err = UploadUsingPAR(zipFileName, parURL)
	if err != nil {
		fmt.Printf("Erro ao fazer upload para o Object Storage usando PAR: %v\n", err)
	} else {
		fmt.Println("Upload concluído com sucesso.")
	}
}

// getAllNamespaces retorna uma lista de todos os namespaces no cluster
func getAllNamespaces(clientset *kubernetes.Clientset) ([]string, error) {
	var namespaceNames []string

	// Obtém a lista de todos os namespaces
	namespaces, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Adiciona cada nome de namespace à lista
	for _, ns := range namespaces.Items {
		namespaceNames = append(namespaceNames, ns.Name)
	}

	return namespaceNames, nil
}

// saveNamespaceResource salva o recurso Namespace como um arquivo YAML.
func saveNamespaceResource(dynClient dynamic.Interface, namespace, originalDir, modifiedDir string, namespaceResourceType schema.GroupVersionResource) error {
	namespaceRes, err := dynClient.Resource(namespaceResourceType).Get(context.TODO(), namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("erro ao obter o recurso Namespace: %v", err)
	}

	// Salva o recurso Namespace original
	originalFilePath := filepath.Join(originalDir, namespace+".yaml")
	err = saveResourceAsYAML(namespaceRes, originalFilePath)
	if err != nil {
		return fmt.Errorf("erro ao salvar recurso Namespace original: %v", err)
	}

	// Cria uma cópia do recurso para modificação
	modifiedNamespaceRes := namespaceRes.DeepCopy()

	// Remove campos indesejados do namespace para a versão modificada
	cleanResource(modifiedNamespaceRes)

	// Salva o recurso Namespace modificado
	modifiedFilePath := filepath.Join(modifiedDir, namespace+".yaml")
	err = saveResourceAsYAML(modifiedNamespaceRes, modifiedFilePath)
	if err != nil {
		return fmt.Errorf("erro ao salvar recurso Namespace modificado: %v", err)
	}

	return nil
}

// cleanResource remove campos indesejados de um recurso Kubernetes.
func cleanResource(resource *unstructured.Unstructured) {
	// Remover campos que não são necessários para recriação
	unstructured.RemoveNestedField(resource.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(resource.Object, "metadata", "uid")
	unstructured.RemoveNestedField(resource.Object, "metadata", "selfLink")
	unstructured.RemoveNestedField(resource.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(resource.Object, "metadata", "generation")
	unstructured.RemoveNestedField(resource.Object, "status")
	unstructured.RemoveNestedField(resource.Object, "metadata", "managedFields")

	// Remover campos específicos de anotações e rótulos
	removeAnnotations(resource, []string{
		"objectset.rio.cattle.io/applied",
		"objectset.rio.cattle.io/id",
		"objectset.rio.cattle.io/hash",
		"cattle.io.timestamp",
	})
	removeLabels(resource, []string{
		"objectset.rio.cattle.io/hash",
	})

	// Remover campos vazios
	removeEmptyFields(resource, []string{
		"metadata.annotations",
		"metadata.labels",
	})
}

// removeAnnotations remove anotações específicas de um recurso Kubernetes.
func removeAnnotations(resource *unstructured.Unstructured, keys []string) {
	annotations := resource.GetAnnotations()
	for _, key := range keys {
		delete(annotations, key)
	}
	resource.SetAnnotations(annotations)
}

// removeLabels remove rótulos específicos de um recurso Kubernetes.
func removeLabels(resource *unstructured.Unstructured, keys []string) {
	labels := resource.GetLabels()
	for _, key := range keys {
		delete(labels, key)
	}
	resource.SetLabels(labels)
}

// removeEmptyFields remove campos vazios de um recurso Kubernetes.
func removeEmptyFields(resource *unstructured.Unstructured, fields []string) {
	for _, field := range fields {
		if value, found, err := unstructured.NestedFieldNoCopy(resource.Object, strings.Split(field, ".")...); err == nil && found {
			if objMap, ok := value.(map[string]interface{}); ok && len(objMap) == 0 {
				unstructured.RemoveNestedField(resource.Object, strings.Split(field, ".")...)
			}
		}
	}
}

// saveResourceAsYAML salva um recurso do Kubernetes como um arquivo YAML.
func saveResourceAsYAML(resource *unstructured.Unstructured, filePath string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Serializa o objeto em YAML
	yamlData, err := yaml.Marshal(resource.Object)
	if err != nil {
		return fmt.Errorf("erro ao codificar recurso para YAML: %v", err)
	}

	_, err = file.Write(yamlData)
	if err != nil {
		return fmt.Errorf("erro ao escrever recurso em arquivo: %v", err)
	}

	return nil
}

// ZipDirectory compacta um diretório em um arquivo ZIP
func ZipDirectory(sourceDir, outputZip string) error {
	// Cria o arquivo ZIP
	zipFile, err := os.Create(outputZip)
	if err != nil {
		return fmt.Errorf("erro ao criar arquivo ZIP: %v", err)
	}
	defer zipFile.Close()

	// Cria um novo writer de ZIP
	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	// Caminha pelo diretório fonte
	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Verifica se é um diretório
		if info.IsDir() {
			return nil
		}

		// Abre o arquivo atual
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("erro ao abrir arquivo %s: %v", path, err)
		}
		defer file.Close()

		// Calcula o caminho dentro do arquivo ZIP
		relPath := strings.TrimPrefix(path, sourceDir)
		zipPath := strings.TrimPrefix(relPath, string(filepath.Separator))

		// Cria um header no ZIP
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return fmt.Errorf("erro ao criar header do arquivo ZIP para %s: %v", path, err)
		}
		header.Name = zipPath
		header.Method = zip.Deflate

		// Adiciona o arquivo ao ZIP
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("erro ao adicionar arquivo ao ZIP %s: %v", path, err)
		}

		// Copia o conteúdo do arquivo para o ZIP
		_, err = io.Copy(writer, file)
		if err != nil {
			return fmt.Errorf("erro ao copiar arquivo %s para o ZIP: %v", path, err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("erro ao caminhar pelo diretório %s: %v", sourceDir, err)
	}

	return nil
}

// UploadUsingPAR faz upload de um arquivo para o OCI Object Storage usando uma URL PAR
func UploadUsingPAR(filePath, parURL string) error {
	// Escapa a URL para garantir que está formatada corretamente
	escapedURL, err := url.Parse(parURL)
	if err != nil {
		return fmt.Errorf("erro ao parsear a URL PAR: %v", err)
	}

	// Imprima a URL e o caminho do arquivo para depuração
	fmt.Println("URL PAR:", escapedURL.String())  // Verificar a URL
	fmt.Println("Arquivo para Upload:", filePath) // Verificar o arquivo

	// Verifique a existência do arquivo
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("erro: o arquivo %s não existe", filePath)
	}

	// Abre o arquivo que será enviado
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("erro ao abrir arquivo %s: %v", filePath, err)
	}
	defer file.Close()

	// Obtém o tamanho do arquivo
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("erro ao obter informações do arquivo %s: %v", filePath, err)
	}
	fileSize := fileInfo.Size()

	// Prepara o buffer para o upload
	buffer := make([]byte, fileSize)
	_, err = file.Read(buffer)
	if err != nil {
		return fmt.Errorf("erro ao ler arquivo %s: %v", filePath, err)
	}

	// Faz upload do arquivo usando a URL PAR
	req, err := http.NewRequest("PUT", escapedURL.String(), bytes.NewReader(buffer))
	if err != nil {
		return fmt.Errorf("erro ao criar request de upload: %v", err)
	}

	// Define o header de tipo de conteúdo
	req.Header.Set("Content-Type", "application/zip")

	// Executa o request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("erro ao enviar arquivo: %v", err)
	}
	defer resp.Body.Close()

	// Verifica se o upload foi bem-sucedido
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("falha no upload, status: %s", resp.Status)
	}

	return nil
}
