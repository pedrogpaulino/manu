# Knowledge Engine Comprehension Specification

## Purpose

Define como o `Knowledge Engine` demonstra compreensão progressiva e verificável de fontes heterogêneas, preservando cobertura, evidências, incerteza, temporalidade e os limites da assistência por IA.

## Requirements

### Requirement: Contrato universal de compreensão
O `Knowledge Engine` MUST representar resultados de analisadores especializados por um contrato conceitual comum que permita expressar, quando sustentados pela fonte, inventário e estrutura, relações, fluxos, decisões, variações configuráveis, capacidades, erros, evolução, documentação, evidências e lacunas. O contrato MUST aceitar contribuições parciais sem exigir que toda fonte forneça todas as dimensões, MUST preservar detalhes específicos que não possam ser normalizados sem perda e MUST permitir reutilização estruturada e verificável por experiências internas e consumidores externos autorizados.

#### Scenario: Correlação entre fontes diferentes
- **WHEN** analisadores de tipos de fonte diferentes identificarem elementos relacionados do mesmo ambiente
- **THEN** o `Knowledge Engine` MUST conseguir correlacionar seus resultados por conceitos comuns, preservando a fonte, o método e o suporte de cada contribuição

#### Scenario: Dimensão não aplicável ou não suportada
- **WHEN** um analisador não puder produzir uma das dimensões do contrato para determinada fonte
- **THEN** o resultado MUST registrar a ausência de cobertura ou suporte sem fabricar conhecimento para preencher a dimensão

#### Scenario: Consumidor externo reutiliza a compreensão
- **WHEN** um consumidor autorizado solicitar contexto estruturado sobre uma entidade, relação ou fluxo compreendido
- **THEN** o `Knowledge Engine` MUST fornecer a contribuição comum com evidências, proveniência, qualificadores, cobertura e lacunas aplicáveis sem exigir que o consumidor interprete o formato privado do analisador de origem

#### Scenario: Normalização perderia semântica específica
- **WHEN** duas linguagens ou plataformas expressarem conceitos parecidos com qualificadores semanticamente diferentes
- **THEN** o contrato MUST preservar o predicado comum sustentado e os qualificadores específicos necessários, sem declarar equivalência mais forte que as fontes permitem

### Requirement: Cobertura progressiva e explícita dos analisadores
Cada análise MUST informar quais dimensões e escopos foram tentados, quais produziram resultados e quais permaneceram não suportados, não aplicáveis, incompletos ou com falha. A presença de um tipo de fonte no catálogo de analisadores MUST NOT ser apresentada como compreensão completa dessa fonte.

#### Scenario: Analisadores com profundidades diferentes
- **WHEN** dois analisadores oferecerem níveis diferentes de compreensão para suas respectivas fontes
- **THEN** as experiências MUST apresentar a cobertura efetivamente alcançada por cada análise sem nivelá-las artificialmente

#### Scenario: Falha parcial de análise
- **WHEN** uma dimensão falhar e outras forem concluídas com sucesso
- **THEN** o conhecimento sustentado MUST permanecer utilizável e a falha parcial MUST ficar visível no contexto de cobertura

### Requirement: Qualificação independente do conhecimento
Uma afirmação relevante MUST preservar separadamente sua origem como conhecimento observado, gerado ou curado; seu suporte e estado de contestação; seu contexto temporal; e, quando descrever comportamento, se representa um caminho possível, uma execução observada ou uma interpretação de processo de negócio. Nenhuma dessas dimensões MUST ser inferida automaticamente a partir de outra.

#### Scenario: Caminho reconstruído estaticamente
- **WHEN** um analisador reconstruir um caminho de código sem dados de execução
- **THEN** o caminho MUST ser apresentado como possível e MUST NOT ser descrito como uma execução ocorrida

#### Scenario: Explicação humana sem evidência técnica direta
- **WHEN** um especialista curar uma justificativa de negócio que não esteja presente nas fontes técnicas
- **THEN** a justificativa MUST permanecer identificada como conhecimento curado, com autoria e contexto, sem ser reclassificada como observação do código

#### Scenario: Fontes contraditórias
- **WHEN** evidências sustentarem afirmações incompatíveis sobre o mesmo contexto
- **THEN** as afirmações MUST permanecer distinguíveis e contestadas até que novas evidências ou uma revisão autorizada estabeleçam seu tratamento

### Requirement: Respostas sustentadas e abstinência explícita
Toda resposta, página, relação ou explicação que possa orientar entendimento, investigação ou mudança MUST apontar para as evidências e a proveniência disponíveis, distinguir inferência de observação e declarar lacunas materiais. Quando o suporte for insuficiente, o Manu MUST limitar ou recusar a conclusão em vez de apresentá-la como fato.

#### Scenario: Pergunta respondida com suporte
- **WHEN** o Manu responder por que um fluxo tomou determinada decisão
- **THEN** a resposta MUST indicar as condições encontradas, suas origens e os elementos de configuração, código, documentação ou curadoria que sustentam a explicação

#### Scenario: Justificativa ausente nas fontes
- **WHEN** as fontes permitirem identificar como uma decisão é executada, mas não por que ela foi escolhida pelo negócio ou pela arquitetura
- **THEN** o Manu MUST separar a explicação mecânica disponível da justificativa ausente e MUST indicar a lacuna

#### Scenario: Evidência protegida
- **WHEN** uma afirmação possuir evidência que o usuário não tem permissão para visualizar
- **THEN** a experiência MUST respeitar a autorização e MUST distinguir evidência protegida de afirmação sem evidência

### Requirement: Perguntas de competência como critério de compreensão
O contrato MUST manter um conjunto versionado de perguntas de competência representativas das necessidades dos públicos autorizados. A avaliação do `Knowledge Engine` MUST usar bases e respostas de referência revisáveis para medir ao menos correção, cobertura, rastreabilidade, atualidade, indicação de incerteza e abstinência apropriada; quantidade de documentação gerada MUST NOT ser usada isoladamente como prova de compreensão.

#### Scenario: Avaliação sobre uma base conhecida
- **WHEN** uma análise do MVP for avaliada sobre uma aplicação com referência preparada por especialistas
- **THEN** as respostas às perguntas de competência MUST ser comparadas com a referência e registrar acertos, omissões, suporte apresentado e conclusões indevidas

#### Scenario: Evolução de um analisador
- **WHEN** um módulo de análise for aprimorado
- **THEN** o mesmo conjunto versionado de perguntas aplicável MUST poder ser executado novamente para evidenciar regressão ou ganho de compreensão

### Requirement: Assistência por IA sem dependência epistemológica
Modelos de IA MUST atuar como recursos autorizados de síntese, explicação, classificação ou consulta sobre conhecimento sustentado. Uma saída de modelo MUST NOT constituir, sozinha, evidência técnica nem transformar inferência em observação. O núcleo MUST continuar oferecendo os resultados determinísticos e curados disponíveis quando a IA estiver indisponível, não configurada ou proibida por política.

#### Scenario: IA disponível e autorizada
- **WHEN** uma política permitir o uso de um modelo para produzir uma explicação
- **THEN** a explicação gerada MUST preservar ligação com seus insumos, indicar sua origem gerada e respeitar as restrições de conteúdo aplicáveis

#### Scenario: IA indisponível ou proibida
- **WHEN** nenhum modelo puder ser usado para uma consulta ou análise
- **THEN** catálogo, estrutura, relações, evidências e demais resultados não dependentes de IA MUST continuar disponíveis, com indicação das experiências limitadas

#### Scenario: Modelo produz afirmação sem suporte
- **WHEN** uma saída de IA introduzir uma afirmação que não possa ser relacionada a evidência ou curadoria disponível
- **THEN** o Manu MUST NOT apresentá-la como conhecimento confirmado da organização

### Requirement: Contexto temporal e comparações qualificadas
Conhecimento dependente de versão ou ambiente MUST preservar contexto suficiente para distinguir ao menos a fonte e sua revisão observada, o instante da análise e, quando disponíveis, ambiente, release, artefato de build, implantação, estado de configuração e revisão documental. Uma comparação MUST declarar quais desses contextos possui e MUST NOT atribuir diferenças a uma causa que não possa sustentar.

#### Scenario: Diferença entre implantações
- **WHEN** um usuário comparar duas implantações com código igual e configurações diferentes
- **THEN** o Manu MUST distinguir a diferença configuracional da diferença de código e apresentar as evidências disponíveis para ambas

#### Scenario: Documentação possivelmente desatualizada
- **WHEN** uma revisão documental estiver relacionada a uma revisão de fonte anterior àquela analisada
- **THEN** o Manu MUST sinalizar possível desatualização ou necessidade de revisão sem sobrescrever silenciosamente o conteúdo curado

#### Scenario: Contexto incompleto de implantação
- **WHEN** a análise conhecer uma revisão de código, mas não conseguir vinculá-la ao artefato efetivamente implantado
- **THEN** o Manu MUST declarar essa lacuna e MUST NOT afirmar que o código analisado representa o ambiente implantado

### Requirement: Capacidades do ambiente e produtos de conhecimento distintos
O domínio MUST distinguir uma `Capability` encontrada no ambiente analisado de um `Knowledge Product` produzido pelo Manu. Uma capacidade MUST descrever algo oferecido ou realizável no ambiente; um produto de conhecimento MUST apresentar ou aplicar conhecimento da base sem ser confundido com a capacidade que documenta.

#### Scenario: Descoberta de relatório existente
- **WHEN** um usuário perguntar qual relatório do ambiente pode atender a uma necessidade
- **THEN** o Manu MUST tratar os relatórios existentes como capacidades ou recursos do ambiente e relacioná-los a acesso, entradas, saídas, evidências e documentação conhecidas

#### Scenario: Relatório de impacto produzido pelo Manu
- **WHEN** o Manu gerar um relatório de impacto sobre uma mudança proposta
- **THEN** o resultado MUST ser identificado como `Knowledge Product` e MUST apontar para as capacidades, relações, claims e evidências utilizadas

### Requirement: Acesso às fontes separado da transferência para IA
O acesso autorizado do `Knowledge Engine` a uma `Source` MUST ser avaliado separadamente da autorização para transferir seu conteúdo a um modelo ou provedor externo. O modo SaaS dedicado e o modo self-hosted MUST aplicar as mesmas políticas conceituais de instalação, fonte e usuário, independentemente do mecanismo físico futuro de conexão.

#### Scenario: Fonte analisável sem transferência externa
- **WHEN** uma política permitir análise dentro da célula, mas proibir o envio do conteúdo da fonte a provedores externos
- **THEN** o Manu MUST produzir o conhecimento que não depende dessa transferência e MUST bloquear somente as etapas não autorizadas

#### Scenario: Usuário sem acesso ao conteúdo original
- **WHEN** uma análise autorizada produzir conhecimento cuja evidência possui visualização restrita para determinado usuário
- **THEN** o Manu MUST aplicar a permissão na consulta sem alterar a proveniência nem ampliar o acesso original

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
