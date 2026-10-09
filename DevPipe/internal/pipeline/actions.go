package pipeline

import (
	"errors"
	"fmt"

	"devpipe/internal/domain"
)

var ErrBusy = errors.New("a demanda está em execução; cancele antes de alterá-la")

func (e *Engine) load(id int64) (domain.Demand, error) {
	d, err := e.st.GetDemand(id)
	if err != nil {
		return d, err
	}
	if e.IsRunning(id) {
		return d, ErrBusy
	}
	return d, nil
}

// Enqueue coloca (ou recoloca) a demanda na fila de execução.
func (e *Engine) Enqueue(id int64) error {
	d, err := e.load(id)
	if err != nil {
		return err
	}
	switch d.Stage {
	case domain.StageDone:
		return errors.New("demanda concluída; use 'Devolver para ajustes' para uma nova revisão")
	case domain.StageReview:
		return errors.New("a demanda aguarda revisão humana: aprove ou devolva para ajustes")
	}
	if d.Stage == domain.StagePlanning && d.Status == domain.StatusAwaitingApproval {
		return errors.New("aprove a especificação antes de continuar")
	}
	if d.Status == domain.StatusQueued {
		return nil
	}
	d.Status, d.StatusMessage = domain.StatusQueued, "Na fila de execução"
	e.touch(&d, "Demanda colocada na fila")
	e.save(&d)
	e.log(d, 0, domain.LevelInfo, "fila", "Demanda colocada na fila de execução", "")
	e.Wake()
	return nil
}

// ApproveSpec aprova (opcionalmente editando) a especificação e continua a esteira.
func (e *Engine) ApproveSpec(id int64, editedSpec string) error {
	d, err := e.load(id)
	if err != nil {
		return err
	}
	rev, err := e.st.GetRevision(id, d.CurrentRevision)
	if err != nil {
		return err
	}
	spec := rev.Spec
	if editedSpec != "" {
		spec = editedSpec
	}
	if spec == "" {
		return errors.New("ainda não há especificação gerada para aprovar")
	}
	if err := e.st.SetRevisionSpec(rev.ID, spec, true); err != nil {
		return err
	}
	edited := ""
	if editedSpec != "" && editedSpec != rev.Spec {
		edited = " (com edições humanas)"
	}
	e.log(d, 0, domain.LevelOK, "aprovacao", "Especificação aprovada"+edited, "")
	if d.Stage == domain.StagePlanning {
		d.Status = domain.StatusIdle
		e.save(&d)
		return e.Enqueue(id)
	}
	return nil
}

// ApproveReview conclui a demanda após inspeção humana. Não faz merge nem push.
func (e *Engine) ApproveReview(id int64, note string) error {
	d, err := e.load(id)
	if err != nil {
		return err
	}
	if d.Stage != domain.StageReview {
		return errors.New("somente demandas em Revisão podem ser aprovadas")
	}
	d.Stage, d.Status, d.Progress = domain.StageDone, domain.StatusDone, 100
	d.StatusMessage = "Aprovada na revisão humana. Branch " + d.WorkBranch + " pronta para merge manual."
	e.touch(&d, "Revisão aprovada")
	e.save(&d)
	e.log(d, 0, domain.LevelOK, "concluido", "Demanda concluída — aprovada na revisão humana. Merge/push não são automáticos.", note)
	return nil
}

// NewAdjustment registra uma devolução para ajustes como nova revisão.
// O chamador deve anexar arquivos à revisão e depois chamar FinishAdjustment.
func (e *Engine) NewAdjustment(id int64, instructions string) (domain.Revision, error) {
	d, err := e.load(id)
	if err != nil {
		return domain.Revision{}, err
	}
	if d.Stage == domain.StageBacklog || (d.Stage == domain.StageAnalysis && d.Status == domain.StatusQueued) {
		return domain.Revision{}, errors.New("a demanda ainda não foi desenvolvida; edite-a diretamente")
	}
	if instructions == "" {
		return domain.Revision{}, errors.New("descreva os ajustes solicitados")
	}
	return e.st.AddRevision(id, instructions)
}

func (e *Engine) FinishAdjustment(id int64, rev domain.Revision, autoQueue bool) error {
	d, err := e.load(id)
	if err != nil {
		return err
	}
	d.CurrentRevision = rev.Number
	d.Stage, d.Status, d.BlockedFrom = domain.StageReturned, domain.StatusIdle, ""
	d.ValidationState = ""
	d.StatusMessage = fmt.Sprintf("Devolvida para ajustes (revisão %d)", rev.Number)
	e.touch(&d, d.StatusMessage)
	e.save(&d)
	e.log(d, 0, domain.LevelWarn, "ajustes", d.StatusMessage, rev.Instructions)
	if autoQueue {
		return e.Enqueue(id)
	}
	return nil
}

// Cancel interrompe uma execução em andamento ou retira da fila.
func (e *Engine) Cancel(id int64) error {
	e.mu.Lock()
	c, running := e.running[id]
	e.mu.Unlock()
	if running {
		c()
		return nil
	}
	d, err := e.st.GetDemand(id)
	if err != nil {
		return err
	}
	if d.Status == domain.StatusQueued {
		d.Status, d.StatusMessage = domain.StatusIdle, "Retirada da fila"
		e.save(&d)
		e.log(d, 0, domain.LevelInfo, "fila", "Demanda retirada da fila", "")
	}
	return nil
}

// Move permite mover manualmente um card parado para etapas de controle.
func (e *Engine) Move(id int64, to domain.Stage) error {
	d, err := e.load(id)
	if err != nil {
		return err
	}
	if d.Status == domain.StatusQueued {
		return errors.New("retire a demanda da fila antes de movê-la")
	}
	switch to {
	case domain.StageBacklog, domain.StageBlocked:
	default:
		return fmt.Errorf("movimentação manual permitida apenas para Backlog ou Bloqueado; as demais etapas são controladas pela esteira")
	}
	from := d.Stage
	if to == domain.StageBlocked {
		d.BlockedFrom = from
	} else {
		d.BlockedFrom = ""
	}
	d.Stage, d.Status = to, domain.StatusIdle
	d.Progress = progressFor(to, d.Progress)
	d.StatusMessage = "Movida manualmente de " + domain.StageLabels[from]
	e.touch(&d, d.StatusMessage)
	e.save(&d)
	e.log(d, 0, domain.LevelInfo, "movimentacao", d.StatusMessage+" para "+domain.StageLabels[to], "")
	return nil
}

// Reorder define a ordem de execução dentro da fila.
func (e *Engine) Reorder(ids []int64) error {
	for i, id := range ids {
		if err := e.st.SetQueueOrder(id, (i+1)*10); err != nil {
			return err
		}
	}
	e.bus.Publish(Message{Type: "reload", Data: nil})
	return nil
}
