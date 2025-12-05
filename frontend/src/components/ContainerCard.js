import "./ContainerCard.css";

function ContainerCard({ container, onAction, onRefresh }) {
  const { name, status, loading } = container;

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

  const containerData = status?.containers?.[0];
  const isRunning = containerData?.State?.toLowerCase() === "running";

  return (
    <div className="container-card">
      <div className="card-header">
        <h2>{name.toUpperCase()}</h2>
        {loading && <div className="spinner"></div>}
      </div>

      {status?.status === "error" ? (
        <div className="error-message">
          <p>❌ {status.message}</p>
        </div>
      ) : containerData ? (
        <div className="card-content">
          <div className="status-row">
            <span className="label">Service:</span>
            <span className="value">{containerData.Service || "N/A"}</span>
          </div>
          <div className="status-row">
            <span className="label">State:</span>
            <span
              className="value state-badge"
              style={{ backgroundColor: getStateColor(containerData.State) }}
            >
              {containerData.State || "Unknown"}
            </span>
          </div>
          <div className="status-row">
            <span className="label">Status:</span>
            <span className="value">{containerData.Status || "N/A"}</span>
          </div>
          <div className="status-row">
            <span className="label">Size:</span>
            <span className="value">{containerData.Size || "0B"}</span>
          </div>
        </div>
      ) : (
        <div className="no-data">
          <p>No container data available</p>
        </div>
      )}

      <div className="card-actions">
        <button
          className="btn btn-start"
          onClick={() => onAction(name, "up")}
          disabled={loading || isRunning}
        >
          ▶️ Start
        </button>
        <button
          className="btn btn-stop"
          onClick={() => onAction(name, "down")}
          disabled={loading || !isRunning}
        >
          ⏹️ Stop
        </button>
        <button
          className="btn btn-refresh"
          onClick={onRefresh}
          disabled={loading}
        >
          🔄 Refresh
        </button>
      </div>
    </div>
  );
}

export default ContainerCard;
