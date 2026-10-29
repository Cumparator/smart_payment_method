const mockReconciling = [
  { id: '9999-0000', amount: 75000, state: 'RECONCILING', nextRun: 'через 5 минут', attempts: 2 },
  { id: '7777-8888', amount: 15000, state: 'MANUAL_REVIEW', nextRun: '-', attempts: 3 },
];

export default function Reconciliation() {
  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">Мониторинг сверки (Reconciliation)</h2>
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <table className="w-full text-left">
          <thead className="bg-gray-50 border-b">
            <tr>
              <th className="p-4">ID Платежа</th>
              <th className="p-4">Статус</th>
              <th className="p-4">Попыток</th>
              <th className="p-4">Следующая проверка</th>
              <th className="p-4">Действие</th>
            </tr>
          </thead>
          <tbody>
            {mockReconciling.map(p => (
              <tr key={p.id} className="border-b">
                <td className="p-4 font-mono text-sm">{p.id}</td>
                <td className="p-4">
                  <span className={`px-2 py-1 rounded text-sm ${p.state === 'MANUAL_REVIEW' ? 'bg-orange-100 text-orange-800' : 'bg-blue-100 text-blue-800'}`}>
                    {p.state}
                  </span>
                </td>
                <td className="p-4">{p.attempts} / 3</td>
                <td className="p-4">{p.nextRun}</td>
                <td className="p-4">
                  {p.state === 'MANUAL_REVIEW' && (
                    <button className="text-blue-600 hover:underline text-sm">Провести вручную</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}