# Knowledge Engine Runtime Specification

## Purpose

Define a execução local, segura, extensível e mensurável da primeira fundação do `Knowledge Engine`, sem antecipar persistência vetorial, IA ou experiências de produto.

## Requirements

### Requirement: Análise local limitada ao recorte autorizado
O runtime MUST analisar somente a raiz, as inclusões e as exclusões explicitamente configuradas para uma `Source`. A análise MUST tratar caminhos, arquivos, links e pacotes como entrada não confiável, MUST NOT executar conteúdo da fonte e MUST NOT alterar a fonte durante descoberta, extração ou benchmark.

#### Scenario: Caminho fora da raiz autorizada
- **WHEN** um caminho, link ou membro de pacote resolver para fora da raiz configurada
- **THEN** o runtime MUST rejeitar o acesso, registrar a falha ou lacuna correspondente e continuar usando somente os resultados autorizados que permanecerem válidos

#### Scenario: Fonte analisada em modo somente leitura
- **WHEN** uma análise ou benchmark for executado sobre um dos recortes do corpus
- **THEN** o conteúdo e os metadados da fonte MUST permanecer inalterados e nenhum arquivo da fonte MUST ser executado

### Requirement: Limites explícitos de trabalho e cancelamento
O runtime MUST aceitar limites para quantidade e tamanho de artefatos, conteúdo lido, membros e expansão de pacotes, concorrência e duração. O runtime MUST interromper trabalho pendente quando houver cancelamento ou limite excedido e MUST distinguir limitação de recurso, falha técnica e dimensão não suportada.

#### Scenario: Pacote excede o limite autorizado
- **WHEN** um CAR declarar quantidade de membros ou tamanho expandido superior ao limite configurado
- **THEN** o runtime MUST interromper a abertura desse pacote sem expandi-lo integralmente, registrar o motivo e preservar resultados concluídos de outros artefatos

#### Scenario: Análise cancelada
- **WHEN** a execução receber cancelamento ou atingir seu prazo
- **THEN** novos trabalhos MUST deixar de ser iniciados, trabalhos cooperativos em andamento MUST ser interrompidos e o resultado MUST identificar a execução como cancelada ou limitada

### Requirement: Resultado comum, rastreável e parcial
Cada execução MUST produzir uma representação estruturada versionada que identifique a `Source`, sua revisão ou hash disponível, o `Analysis Snapshot`, os `Artifact`s analisados, os analisadores e métodos aplicados e as contribuições produzidas. Observações, relações, `Evidence`, `Analysis Coverage`, `Explicit Gap`s e falhas MUST manter locadores e proveniência suficientes para inspeção, sem transformar falha parcial em perda dos resultados válidos.

#### Scenario: Analisador produz contribuição verificável
- **WHEN** um analisador identificar estrutura ou relação em um artefato
- **THEN** o resultado MUST relacionar a contribuição ao artefato, ao hash, ao analisador, ao método e a um locador verificável

#### Scenario: Falha parcial de um analisador
- **WHEN** um analisador falhar em um artefato ou dimensão depois que outras contribuições forem concluídas
- **THEN** as contribuições válidas MUST permanecer disponíveis e a cobertura MUST expor separadamente a falha e as lacunas resultantes

### Requirement: Fallback genérico e especialização aditiva
Todo artefato textual autorizado e suportado pelo runtime MUST receber descoberta, identidade, hash, classificação e extração genérica aplicável. Analisadores especializados MUST acrescentar contribuições ao mesmo resultado comum sem apagar o fallback, a proveniência, a cobertura ou as lacunas de outras contribuições.

#### Scenario: Artefato sem especialização disponível
- **WHEN** um artefato textual autorizado não possuir analisador especializado aplicável
- **THEN** o runtime MUST preservar o resultado genérico disponível e MUST declarar como não suportadas as dimensões que exigiriam especialização

#### Scenario: Mais de um analisador é aplicável
- **WHEN** analisadores genérico, de linguagem, de framework ou de pacote examinarem o mesmo artefato
- **THEN** o resultado MUST manter suas contribuições e métodos distinguíveis e MUST correlacioná-los sem substituição silenciosa

### Requirement: Fronteira extensível de analisadores
O runtime MUST permitir registrar e selecionar analisadores por tipo de fonte, artefato ou capacidade declarada sem criar um pipeline de ingestão independente para cada linguagem. Uma falha, ausência ou incompatibilidade de um analisador MUST NOT impedir a aplicação dos demais analisadores compatíveis nem do fallback genérico.

#### Scenario: Novo analisador compatível é registrado
- **WHEN** um analisador declarar suporte a um tipo de artefato e ao contrato de resultado vigente
- **THEN** ele MUST poder participar do pipeline comum sem alterar a descoberta, o fallback ou os consumidores do resultado estruturado

#### Scenario: Analisador incompatível ou indisponível
- **WHEN** um analisador não puder ser carregado ou não suportar a versão do contrato exigida
- **THEN** o runtime MUST recusar somente essa contribuição, registrar a incompatibilidade e continuar com os analisadores compatíveis

### Requirement: Superfície operacional automatizável
A CLI inicial MUST permitir analisar um recorte local, inspecionar o resultado e sua cobertura e executar o benchmark do microcorte. Cada operação MUST oferecer saída concisa para pessoas e uma saída estruturada versionada para automação, com códigos de saída que distingam sucesso, resultado parcial, entrada inválida e falha técnica.

#### Scenario: Automação recebe resultado parcial
- **WHEN** uma análise produzir inventário e evidências válidos, mas também dimensões não suportadas ou falhas parciais
- **THEN** a saída estruturada e o código de saída MUST permitir distinguir esse estado de sucesso completo e de falha técnica total

#### Scenario: Pessoa inspeciona uma execução
- **WHEN** a CLI for executada sem solicitar saída estruturada
- **THEN** ela MUST resumir recorte, revisão, artefatos, cobertura, lacunas, falhas e métricas sem exigir leitura do formato interno completo

### Requirement: Benchmark reproduzível e incremental
O benchmark MUST identificar ambiente, configuração, corpus e revisão ou hash e MUST medir ao menos duração por etapa, memória de pico pelo método disponível, volume de saída, concorrência efetiva, artefatos descobertos, reutilizados e reprocessados e falhas. Ele MUST distinguir primeira análise, repetição sem mudança e atualização localizada sem alterar a fonte original nem comunicar o resultado como SLA.

#### Scenario: Repetição sem mudança
- **WHEN** o mesmo recorte e configuração forem analisados novamente sem mudança de conteúdo
- **THEN** o benchmark MUST registrar o trabalho reutilizado e reprocessado e os resultados factuais MUST permanecer equivalentes, salvo metadados próprios da execução

#### Scenario: Atualização localizada
- **WHEN** uma alteração localizada autorizada for apresentada sobre a mesma linha de base
- **THEN** o benchmark MUST identificar os artefatos alterados e dependentes reprocessados, preservar os não afetados e registrar a limitação de qualquer simulação usada

### Requirement: Execução inicial portável em Linux e contêiner
O runtime inicial MUST poder ser distribuído como executável Linux e imagem de contêiner, executar sem privilégios administrativos e receber a fonte por montagem somente leitura. Essa distribuição MUST NOT exigir banco de dados principal, modelo de IA ou serviço de cloud dentro do processo local para produzir os resultados determinísticos deste microcorte.

#### Scenario: Execução em contêiner sem IA ou banco
- **WHEN** a imagem for executada por usuário não privilegiado com uma fonte montada em modo somente leitura
- **THEN** descoberta, hashing, análise determinística e benchmark MUST funcionar sem credencial de modelo, banco principal ou acesso a um serviço de cloud

#### Scenario: Escrita fora dos destinos permitidos
- **WHEN** o processo precisar produzir saída ou dados temporários
- **THEN** ele MUST escrever somente nos destinos configurados para esse fim e MUST NOT usar a montagem da fonte como diretório de trabalho
