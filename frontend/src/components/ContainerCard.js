import { useState } from "react";
import "./ContainerCard.css";

function ContainerCard({ service, status, onAction }) {
  const [actionLoading, setActionLoading] = useState(false);

  const getStateColor = (state) => {
    if (!state) return "#999";
    switch (state.toLowerCase()) {
      case "running":
        return "#4caf50";
      case "exited":
        return "#f44336";
      case "paused":
        return "#ff9800";
      default:
        return "#999";
    }
  };

  const isDown = !status;
  const isRunning = status?.State?.toLowerCase() === "running";

  return (
    <div className={`container-card ${isDown ? "container-down" : ""}`}>
      <div className="card-header">
        <h2>{service}</h2>
        {status ? (
          <span
            className="status-badge"
            style={{ backgroundColor: getStateColor(status.State) }}
          >
            {status.State || "Unknown"}
          </span>
        ) : (
          <span className="status-badge" style={{ backgroundColor: "#999" }}>
            DOWN
          </span>
        )}
      </div>

      {status && (
        <div className="card-content">
          <div className="status-row">
            <span className="label">Status:</span>
            <span className="value">{status.Status || "N/A"}</span>
          </div>
          <div className="status-row">
            <span className="label">Name:</span>
            <span className="value">{status.Name || "N/A"}</span>
          </div>
        </div>
      )}

      {isDown && (
        <div className="card-content">
          <p className="down-message">Container is not running</p>
        </div>
      )}

      <div className="card-actions">
        {isDown ? (
          <>
            <button
              className="btn btn-up"
              onClick={async () => {
                setActionLoading(true);
                await onAction(service, "up");
                setActionLoading(false);
              }}
              disabled={actionLoading}
            >
              🚀 Bring Up
            </button>
            <button
              className="btn btn-pull"
              onClick={async () => {
                setActionLoading(true);
                await onAction(service, "pull");
                setActionLoading(false);
              }}
              disabled={actionLoading}
            >
              ⬇️ Pull
            </button>
          </>
        ) : (
          <>
            <button
              className="btn btn-start"
              onClick={async () => {
                setActionLoading(true);
                await onAction(service, "start");
                setActionLoading(false);
              }}
              disabled={actionLoading || isRunning}
            >
              ▶️ Start
            </button>
            <button
              className="btn btn-restart"
              onClick={async () => {
                setActionLoading(true);
                await onAction(service, "restart");
                setActionLoading(false);
              }}
              disabled={actionLoading || !isRunning}
            >
              🔄 Restart
            </button>
            <button
              className="btn btn-stop"
              onClick={async () => {
                setActionLoading(true);
                await onAction(service, "stop");
                setActionLoading(false);
              }}
              disabled={actionLoading || !isRunning}
            >
              ⏹️ Stop
            </button>
            <button
              className="btn btn-down"
              onClick={async () => {
                setActionLoading(true);
                await onAction(service, "down");
                setActionLoading(false);
              }}
              disabled={actionLoading}
            >
              🗑️ Remove
            </button>
            <button
              className="btn btn-pull"
              onClick={async () => {
                setActionLoading(true);
                await onAction(service, "pull");
                setActionLoading(false);
              }}
              disabled={actionLoading}
            >
              ⬇️ Pull
            </button>
          </>
        )}
      </div>
    </div>
  );
}

export default ContainerCard;
