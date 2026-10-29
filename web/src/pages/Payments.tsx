const mockPayments = [
  { id: '1111-2222', amount: 50000, state: 'SUCCEEDED', strategy: 'expected_cost', channel: 'BANK_B' },
  { id: '3333-4444', amount: 12000, state: 'FAILED', strategy: 'cheapest', channel: 'BANK_A' },
];

export default function Payments() {
  return (
    <div>
      <h2 className="text-2xl font-bold mb-6">Последние платежи</h2>
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <table className="w-full text-left">
          <thead className="bg-gray-50 border-b">
            <tr>
              <th className="p-4">ID Платежа</th>
              <th className="p-4">Сумма (коп)</th>
              <th className="p-4">Статус</th>
              <th className="p-4">Стратегия</th>
              <th className="p-4">Объяснение маршрутизатора</th>
            </tr>
          </thead>
          <tbody>
            {mockPayments.map(p => (
              <tr key={p.id} className="border-b">
                <td className="p-4 font-mono text-sm">{p.id}</td>
                <td className="p-4">{p.amount}</td>
                <td className="p-4">
                  <span className={`px-2 py-1 rounded text-sm ${p.state === 'SUCCEEDED' ? 'bg-green-100 text-green-800' : 'bg-red-100 text-red-800'}`}>
                    {p.state}
                  </span>
                </td>
                <td className="p-4 font-mono text-sm">{p.strategy}</td>
                <td className="p-4 text-sm text-gray-600">
                  {p.strategy === 'expected_cost' 
                    ? `Выбран ${p.channel}, так как ожидаемая цена (комиссия + риск штрафа) ниже на 40 коп.`
                    : `Выбран ${p.channel}, так как тариф минимален (1%).`}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}