## Purpose

Define o primeiro fluxo consultável do Manu, no qual resultados autorizados do Agent são ingeridos, recuperados como evidências e usados por provedores substituíveis para produzir respostas citadas e explicitamente limitadas.

## ADDED Requirements

### Requirement: Bundle de análise versionado, íntegro e limitado
O Manu MUST aceitar um `Analysis Bundle` versionado que preserve a identidade da `Organization`, `Source`, revisão ou hash, `Analysis Snapshot`, artefatos, contribuições, cobertura, lacunas, falhas e unidades de evidência autorizadas. O bundle MUST possuir integridade verificável, limites explícitos e conteúdo suficiente para reconstruir o conhecimento suportado sem incluir arquivos-fonte inteiros por padrão.

#### Scenario: Bundle válido produzido pelo Agent
- **WHEN** o Agent enviar um bundle compatível cujas identidades, hashes, referências e limites forem válidos
- **THEN** a plataforma MUST aceitar o bundle para ingestão e MUST preservar sua proveniência sem reinterpretar uma contribuição como evidência de execução

#### Scenario: Bundle incompatível ou inconsistente
- **WHEN** o bundle possuir versão não suportada, hash divergente, referência órfã, caminho inválido ou exceder um limite configurado
- **THEN** a plataforma MUST rejeitar ou limitar a ingestão com erro estruturado, MUST NOT publicar o conteúdo inválido e MUST preservar o diagnóstico sem registrar o conteúdo sensível

### Requirement: Unidades de evidência semanticamente delimitadas e autorizadas
Cada `Evidence Unit` MUST representar somente o material mínimo necessário para sustentar ou contestar uma observação, relação ou resposta, mantendo identidade, conteúdo ou estado de redação, hash, locador, artefato, snapshot, método e política de transferência. Conteúdo da fonte MUST ser tratado como entrada não confiável e MUST NOT ser enviado a um provedor quando a política aplicável não autorizar a transferência.

#### Scenario: Evidência autorizada para recuperação e transferência
- **WHEN** um analisador produzir um trecho limitado cuja política permita persistência e envio ao provedor configurado
- **THEN** o Manu MUST relacionar o trecho à sua origem e MUST poder usá-lo na recuperação e no pacote de evidências sem ampliar seu conteúdo

#### Scenario: Evidência disponível localmente mas proibida para IA
- **WHEN** a política permitir persistir e consultar uma evidência localmente, mas proibir sua transferência externa
- **THEN** o Manu MUST excluir ou redigir essa evidência do pacote enviado ao provedor, MUST manter os resultados locais autorizados e MUST declarar a limitação da resposta

### Requirement: Ingestão assíncrona, idempotente e inspecionável
A API MUST criar ingestões identificáveis e expor seus estados `pending`, `running`, `completed`, `partial` ou `failed`. O mesmo bundle factual submetido novamente no mesmo escopo MUST NOT duplicar conhecimento ou chamadas externas, e uma falha de projeção MUST NOT apagar o conhecimento canônico válido já aceito.

#### Scenario: Ingestão aceita para processamento
- **WHEN** um cliente autorizado pelo modo local submeter um bundle bem formado
- **THEN** a API MUST retornar aceitação e um identificador que permita consultar progresso, contagens, falhas, limitações e resultado final

#### Scenario: Reenvio idempotente
- **WHEN** o mesmo bundle e a mesma configuração factual forem submetidos novamente
- **THEN** a plataforma MUST retornar o resultado existente ou reutilizar o trabalho compatível sem criar observações, evidências ou embeddings duplicados

#### Scenario: Embedding indisponível durante a ingestão
- **WHEN** o conhecimento canônico e as projeções não vetoriais forem persistidos, mas o provedor de embeddings falhar ou estiver proibido
- **THEN** a ingestão MUST permanecer utilizável como parcial, MUST expor a limitação vetorial e MUST permitir reconstruir essa projeção posteriormente

### Requirement: Fonte de verdade preservada e projeções reconstruíveis
O Manu MUST persistir separadamente a representação canônica de fontes, snapshots, artefatos, observações, entidades, relações, evidências, cobertura, lacunas e falhas. Índices textuais, relacionais e vetoriais MUST ser projeções substituíveis e reconstruíveis, e uma nova análise MUST preservar histórico enquanto atualiza a visão ativa de modo identificável.

#### Scenario: Projeção vetorial reconstruída
- **WHEN** o perfil de embedding for alterado ou a projeção vetorial for removida
- **THEN** o Manu MUST poder recriá-la a partir das unidades de evidência autorizadas sem alterar observações, relações ou proveniência canônicas

#### Scenario: Atualização localizada da fonte
- **WHEN** uma nova análise alterar somente parte dos artefatos de uma `Source`
- **THEN** a plataforma MUST invalidar ou substituir apenas as projeções derivadas afetadas, MUST preservar itens não afetados e MUST manter o snapshot anterior consultável para rastreabilidade

### Requirement: API HTTP versionada e confinada no modo sem autenticação
O modo servidor MUST oferecer contratos HTTP versionados para criar e consultar ingestões, executar e recuperar consultas, inspecionar evidências e informar saúde e prontidão. Enquanto não existir autenticação, o servidor MUST usar uma única `Organization` configurada, MUST escutar somente em interface de loopback por padrão e MUST recusar exposição não local.

#### Scenario: Cliente local usa a API versionada
- **WHEN** um cliente local chamar um endpoint suportado com conteúdo válido
- **THEN** a API MUST produzir resposta JSON versionada, identificadores rastreáveis e códigos HTTP que distingam aceitação, sucesso, parcialidade, entrada inválida, conflito e falha técnica

#### Scenario: Tentativa de exposição remota sem autenticação
- **WHEN** a configuração tentar iniciar o servidor sem autenticação em endereço que não seja de loopback
- **THEN** o processo MUST recusar a inicialização com diagnóstico seguro em vez de expor ingestão, consulta ou evidências à rede

### Requirement: Recuperação híbrida limitada e degradável
Uma consulta MUST combinar, quando disponíveis, correspondência exata ou textual, similaridade semântica e expansão de relações sustentadas, preservando identidade, proveniência e escopo. O processo MUST aplicar limites reproduzíveis, MUST NOT misturar vetores incompatíveis e MUST continuar oferecendo recuperação textual e relacional quando embeddings estiverem ausentes.

#### Scenario: Sinais complementares encontram evidências
- **WHEN** termos exatos, similaridade semântica ou relações diretas encontrarem candidatos relevantes no mesmo escopo autorizado
- **THEN** o Manu MUST combinar os sinais de modo determinístico e MUST devolver candidatos distinguíveis com suas origens e pontuações explicáveis

#### Scenario: Recuperação sem embeddings
- **WHEN** a projeção vetorial estiver indisponível, incompleta ou proibida
- **THEN** a consulta MUST usar os sinais textuais e relacionais disponíveis, MUST indicar a degradação e MUST NOT apresentar a ausência do vetor como ausência de conhecimento

### Requirement: Pacote de evidências autorizado antes da geração
Antes de chamar um modelo de geração, o Manu MUST formar um pacote limitado contendo somente evidências autorizadas, locadores, relações, proveniência, cobertura e lacunas necessárias para a pergunta. O provedor MUST NOT receber acesso direto à fonte, ao banco ou a conteúdo fora desse pacote.

#### Scenario: Pacote respeita orçamento e diversidade
- **WHEN** a recuperação produzir mais candidatos que o limite da consulta
- **THEN** o Manu MUST selecionar um conjunto limitado e reproduzível, evitar concentração indevida em um único artefato e registrar quais candidatos entraram ou foram excluídos

#### Scenario: Nenhuma evidência transferível sustenta a pergunta
- **WHEN** os resultados locais existirem, mas nenhuma evidência autorizada puder sustentar a geração solicitada
- **THEN** o Manu MUST bloquear a chamada externa e retornar uma resposta de abstinência com a lacuna e a restrição aplicáveis

### Requirement: AI Gateway portátil e capacidades separadas
O `AI Gateway` MUST separar as capacidades de embeddings e geração e MUST permitir configurá-las com provedores e modelos diferentes. O núcleo MUST usar contratos internos independentes de fornecedor, validar capacidades antes da chamada e normalizar identidade do modelo, uso, latência, término e erros sem apagar metadados específicos necessários à auditoria.

#### Scenario: OpenAI configurada diretamente
- **WHEN** a instalação configurar OpenAI para uma capacidade e fornecer credencial externamente ao processo
- **THEN** o adaptador MUST executar somente a operação autorizada e MUST registrar provedor, modelo efetivo, uso e resultado sem persistir ou expor a credencial

#### Scenario: Provedor compatível configurado
- **WHEN** a instalação configurar um endpoint compatível suportado, como OpenRouter, com modelo e capacidades declaradas
- **THEN** o mesmo fluxo de domínio MUST funcionar sem tipos do provedor atravessarem a fronteira do gateway, e uma diferença de capacidade MUST ser rejeitada ou declarada em vez de ignorada

#### Scenario: Provedores distintos para embedding e geração
- **WHEN** a instalação selecionar provedores ou modelos diferentes para embeddings e geração
- **THEN** cada chamada MUST usar sua configuração própria e a resposta MUST manter rastreabilidade independente para as duas capacidades

### Requirement: Resposta gerada citada e abstinência verificável
Uma resposta assistida MUST ser registrada como `Generated knowledge`, decomposta em afirmações relevantes e relacionada às evidências utilizadas. O Manu MUST distinguir observações, inferências e lacunas e MUST limitar ou recusar qualquer conclusão que o pacote não sustente.

#### Scenario: Resposta sustentada
- **WHEN** o pacote contiver evidências suficientes para responder uma pergunta de competência
- **THEN** a resposta MUST citar identificadores e locadores consultáveis, MUST preservar os limites de cobertura e MUST permitir inspecionar o suporte de cada afirmação relevante

#### Scenario: Modelo introduz afirmação sem suporte
- **WHEN** a saída do provedor contiver uma afirmação que não possa ser relacionada às evidências do pacote
- **THEN** o Manu MUST marcar ou remover a afirmação como não sustentada e MUST NOT apresentá-la como conhecimento confirmado da organização

#### Scenario: Pergunta exige comportamento não observado
- **WHEN** a pergunta solicitar uma execução ocorrida, causa raiz ou intenção de negócio e existirem somente caminhos estáticos ou inventário
- **THEN** a resposta MUST se abster dessa conclusão, explicar a distinção e citar as lacunas relevantes

### Requirement: Avaliação simulada por padrão e execução real sob orçamento
O fluxo MUST poder ser avaliado sem chamadas externas por meio de provedores simulados determinísticos. Uma `live eval` MUST exigir ativação explícita, política de transferência e orçamento, e MUST registrar separadamente extração, recuperação, geração, latência, uso, custo e falhas sem expor segredos.

#### Scenario: Regressão executada sem provedor externo
- **WHEN** a suíte padrão avaliar ingestão, projeções, recuperação, pacote, citações e abstinência
- **THEN** ela MUST usar respostas simuladas reproduzíveis e MUST atribuir falhas à etapa responsável sem custo externo

#### Scenario: Live eval autorizada
- **WHEN** uma avaliação real for solicitada com provedor, modelo, evidências transferíveis e orçamento válidos
- **THEN** o Manu MUST limitar consumo, registrar modelo efetivo, tokens, custo, latência e conteúdo identificado por hash e MUST interromper novas chamadas quando o orçamento for excedido

### Requirement: Célula local operável e observável
O corte MUST poder ser iniciado como uma célula local contendo aplicação e persistência, com configuração externa, migrações verificáveis, volume persistente e verificações de saúde e prontidão. A ausência de interface web, autenticação ou serviços opcionais MUST NOT impedir ingestão, recuperação e consulta local.

#### Scenario: Célula local saudável
- **WHEN** a aplicação e a persistência iniciarem com schema compatível e configuração válida
- **THEN** a verificação de prontidão MUST indicar disponibilidade somente depois que o banco e as migrações necessárias estiverem prontos

#### Scenario: Persistência incompatível
- **WHEN** a aplicação encontrar schema ausente, adiantado ou incompatível que não possa migrar com segurança
- **THEN** ela MUST permanecer não pronta e MUST apresentar diagnóstico sem executar alteração destrutiva silenciosa
