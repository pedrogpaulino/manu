## ADDED Requirements

### Requirement: Corpus de referência heterogêneo e reproduzível
O primeiro corte vertical MUST manter um corpus versionado que combine uma aplicação Java/Quarkus, pacotes declarativos WSO2 e uma aplicação Python/Frappe, preservando para cada recorte sua identidade, revisão ou hash verificável, inclusões, exclusões, finalidade de avaliação e autorização de processamento e transferência. A presença no corpus MUST NOT implicar profundidade semântica uniforme entre as fontes.

#### Scenario: Execução sobre uma revisão conhecida
- **WHEN** uma avaliação usar uma aplicação ou pacote do corpus de referência
- **THEN** o resultado MUST identificar o recorte e a revisão ou hash efetivamente analisados, bem como exclusões e limitações aplicáveis

#### Scenario: Cobertura diferente entre aplicações
- **WHEN** a aplicação Java/Quarkus possuir análise semântica mais profunda que os recortes WSO2 ou Python/Frappe
- **THEN** a avaliação MUST comparar apenas as dimensões tentadas em cada recorte e MUST manter visível a diferença de cobertura

### Requirement: Análise extensível com fallback genérico
O primeiro corte vertical MUST usar um único fluxo de compreensão ao qual analisadores especializados possam contribuir sem impor contratos isolados. Fontes textuais autorizadas MUST receber ao menos inventário e extração genérica aplicável, enquanto contribuições especializadas MUST acrescentar semântica, relações e cobertura sem apagar observações ou lacunas anteriores.

#### Scenario: Fonte sem analisador especializado
- **WHEN** um tipo de fonte autorizado não possuir analisador especializado no corte
- **THEN** o `Knowledge Engine` MUST produzir os resultados genéricos sustentados disponíveis e MUST declarar como não suportadas as dimensões que dependeriam da especialização ausente

#### Scenario: Múltiplos analisadores sobre a mesma fonte
- **WHEN** analisadores de linguagem, framework, configuração ou documento contribuírem para os mesmos artefatos
- **THEN** o resultado MUST correlacionar as contribuições pelo contrato comum, preservando o método, a cobertura e as evidências de cada analisador

### Requirement: Projeções recuperáveis sem reduzir o conhecimento a vetores
O conhecimento produzido no corte MUST permanecer recuperável por conteúdo textual, similaridade semântica e relações sustentadas, preservando entidades, observações, evidências, proveniência, cobertura e lacunas fora da representação vetorial. A atualização ou substituição de uma projeção MUST NOT transformar uma saída de embedding em fonte de verdade do conhecimento.

#### Scenario: Recuperação híbrida de evidências
- **WHEN** uma pergunta possuir termos exatos, conceitos semanticamente relacionados e relações conhecidas entre artefatos
- **THEN** a recuperação MUST poder combinar esses sinais e retornar evidências com identidade e proveniência consultáveis

#### Scenario: Embedding indisponível
- **WHEN** a geração ou consulta de embeddings estiver indisponível, proibida ou incompleta
- **THEN** inventário, conteúdo textual, relações e evidências já produzidos MUST permanecer utilizáveis e a limitação da recuperação semântica MUST ficar visível

### Requirement: Consulta assistida por IA sobre pacote de evidências
O corte MUST permitir uma consulta em linguagem natural que recupere primeiro um pacote limitado de evidências autorizadas e somente então solicite ao `AI Gateway` uma resposta gerada. A resposta MUST distinguir afirmações sustentadas, inferências e lacunas, referenciar as evidências utilizadas e recusar conclusões para as quais o pacote não forneça suporte suficiente.

#### Scenario: Resposta sustentada pelo índice
- **WHEN** a recuperação encontrar evidências suficientes para uma pergunta de competência
- **THEN** a resposta gerada MUST usar apenas o conhecimento autorizado fornecido, citar as evidências relevantes e ser identificada como conhecimento gerado

#### Scenario: Recuperação insuficiente
- **WHEN** o índice não fornecer suporte suficiente para uma conclusão solicitada
- **THEN** a resposta MUST declarar a insuficiência e as lacunas materiais sem usar conhecimento geral do modelo como se fosse evidência da organização

#### Scenario: Provedor externo não autorizado
- **WHEN** a fonte puder ser analisada localmente mas sua política impedir a transferência do pacote de evidências ao provedor configurado
- **THEN** a consulta dependente de IA MUST ser bloqueada sem impedir o acesso autorizado aos resultados não dependentes de IA

### Requirement: Superfície operacional inicial por CLI
O primeiro corte MUST oferecer uma interface de linha de comando que permita configurar um recorte local autorizado, iniciar sua análise, consultar o estado e a cobertura, fazer uma pergunta, inspecionar as evidências de uma resposta e executar avaliações e benchmarks. As operações MUST possuir saída legível por pessoa e uma representação estruturada adequada a automação.

#### Scenario: Inspeção após análise parcial
- **WHEN** uma análise concluir algumas dimensões e falhar ou não suportar outras
- **THEN** a CLI MUST permitir consultar os resultados produzidos, a cobertura, as falhas e as lacunas sem representar a execução inteira como compreensão completa

#### Scenario: Automação de uma avaliação
- **WHEN** uma avaliação for executada por script
- **THEN** a CLI MUST retornar resultado estruturado suficiente para relacionar corpus, execução, perguntas, evidências, métricas e falhas

### Requirement: Avaliação em camadas e repetível
O corte MUST separar a avaliação de extração, recuperação e geração de respostas, usando referências revisáveis e execuções identificáveis. Testes determinísticos de analisadores e contratos MUST poder ser executados sem chamadas reais ao provedor, enquanto avaliações autorizadas com modelo real MUST registrar modelo, configuração, insumos, saída, tokens, custo e latência.

#### Scenario: Regressão de um analisador sem IA
- **WHEN** um analisador for alterado
- **THEN** seus casos determinísticos e perguntas aplicáveis MUST poder ser repetidos sem provedor externo para detectar mudanças em entidades, relações, evidências, cobertura e lacunas

#### Scenario: Avaliação de recuperação separada da redação
- **WHEN** uma resposta final estiver incorreta ou incompleta
- **THEN** a avaliação MUST permitir distinguir falha de extração, falha de recuperação e falha de geração

#### Scenario: Chamada real sob orçamento
- **WHEN** uma suíte autorizada usar embeddings ou geração externos
- **THEN** a execução MUST aplicar um orçamento configurado e registrar consumo e custo sem expor credenciais ou conteúdo não autorizado

### Requirement: Benchmark do corte orientado a recursos e evolução
O corte MUST medir tempo, pico de memória, volume persistido, custo externo e latência de ingestão e consulta sobre recortes identificados do corpus. O benchmark MUST distinguir primeira análise de reanálise sem mudança e de reanálise incremental, sem promover seus resultados iniciais a SLA comercial.

#### Scenario: Comparação de reanálise
- **WHEN** o mesmo recorte for analisado novamente sem mudanças ou com uma alteração localizada
- **THEN** o benchmark MUST registrar o trabalho reutilizado e reprocessado, além das diferenças de tempo, memória, armazenamento e custo

#### Scenario: Resultado usado para escolher stack
- **WHEN** alternativas de stack ou mecanismo forem comparadas antes da implementação definitiva
- **THEN** a decisão MUST usar o mesmo corpus, operações e métricas do corte e MUST preservar limitações que impeçam comparação equivalente
